//! One device's WebSocket connection: a read loop on the handler's task, a
//! writer task over a bounded queue, and a task per request.

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::extract::State;
use axum::extract::ws::{CloseFrame, Message, WebSocket, WebSocketUpgrade};
use axum::response::Response;
use bytes::Bytes;
use futures_util::{SinkExt, StreamExt};
use prost::Message as _;
use tokio::sync::mpsc;

use crate::App;
use crate::pb::backend::v1::{AckRequest, CallRequest};
use crate::pb::v1::{
    self as pb, ErrorCode, Frame, Request, Response as WireResponse, frame, request, response,
};

const MAX_FRAME_BYTES: usize = 64 * 1024;
const OUTBOUND_QUEUE: usize = 32;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);

#[derive(Clone, Copy, Default)]
struct Identity {
    user_id: u64,
    device_id: u64,
    connected: bool,
}

pub struct Conn {
    app: Arc<App>,
    out: mpsc::Sender<Bytes>,
    identity: Mutex<Identity>,
}

pub async fn upgrade(ws: WebSocketUpgrade, State(app): State<Arc<App>>) -> Response {
    ws.max_message_size(MAX_FRAME_BYTES)
        .read_buffer_size(1024)
        .write_buffer_size(0)
        .on_upgrade(move |socket| serve(socket, app))
}

async fn serve(socket: WebSocket, app: Arc<App>) {
    app.metrics.connection(1);
    let (mut sink, mut stream) = socket.split();
    let (tx, mut rx) = mpsc::channel::<Bytes>(OUTBOUND_QUEUE);
    let conn = Arc::new(Conn {
        app: app.clone(),
        out: tx,
        identity: Mutex::new(Identity::default()),
    });

    let writer = tokio::spawn(async move {
        while let Some(data) = rx.recv().await {
            if sink.send(Message::Binary(data)).await.is_err() {
                break;
            }
        }
        let _ = sink.close().await;
    });

    while let Some(Ok(message)) = stream.next().await {
        match message {
            Message::Binary(data) => {
                if !conn.receive(data).await {
                    break;
                }
            }
            Message::Text(_) => {
                conn.close(1003, "frames are binary protobuf").await;
                break;
            }
            Message::Close(_) => break,
            _ => {}
        }
    }
    conn.closed().await;
    drop(conn);
    let _ = writer.await;
    app.metrics.connection(-1);
}

impl Conn {
    fn identity(&self) -> Identity {
        *self.identity.lock().unwrap()
    }

    /// Handles one frame; false ends the connection.
    async fn receive(self: &Arc<Self>, data: Bytes) -> bool {
        let Ok(frame) = Frame::decode(data) else {
            self.close(1008, "malformed frame").await;
            return false;
        };
        match frame.kind {
            Some(frame::Kind::Request(request)) => {
                let conn = self.clone();
                tokio::spawn(async move { conn.handle_request(request).await });
            }
            Some(frame::Kind::Ack(ack)) => {
                let id = self.identity();
                if id.connected {
                    let mut backend = self.app.backend.clone();
                    tokio::spawn(async move {
                        let req = AckRequest {
                            user_id: id.user_id,
                            device_id: id.device_id,
                            cursor: ack.cursor,
                        };
                        if let Err(e) = backend.ack(req).await {
                            tracing::warn!(error = %e, "ack failed");
                        }
                    });
                }
            }
            Some(frame::Kind::Ping(_)) => {
                self.send_frame(Frame {
                    kind: Some(frame::Kind::Pong(pb::Pong {})),
                })
                .await;
            }
            Some(frame::Kind::Pong(_)) => {}
            _ => {
                self.close(1008, "unexpected frame").await;
                return false;
            }
        }
        true
    }

    async fn handle_request(self: Arc<Self>, request: Request) {
        let started = Instant::now();
        let kind = request_type(&request);
        let response = tokio::time::timeout(REQUEST_TIMEOUT, self.dispatch(request))
            .await
            .unwrap_or_else(|_| error(0, ErrorCode::Unavailable, "services unavailable"));
        let code = match &response.body {
            Some(response::Body::Error(e)) => e.code().as_str_name().to_string(),
            _ => "ok".to_string(),
        };
        self.send_frame(Frame {
            kind: Some(frame::Kind::Response(response)),
        })
        .await;
        self.app.metrics.request(kind, &code, started.elapsed());
    }

    /// Applies the connection's state rules, then forwards to the services.
    async fn dispatch(&self, request: Request) -> WireResponse {
        let id = self.identity();
        let is_connect = matches!(request.body, Some(request::Body::Connect(_)));
        if is_connect && id.connected {
            return error(
                request.id,
                ErrorCode::FailedPrecondition,
                "already connected",
            );
        }
        if !is_connect && !id.connected {
            return error(request.id, ErrorCode::Unauthenticated, "connect first");
        }
        let req = CallRequest {
            user_id: id.user_id,
            device_id: id.device_id,
            request: Some(request.clone()),
        };
        let mut backend = self.app.backend.clone();
        let response = match backend.call(req).await {
            Ok(r) => r.into_inner().response.unwrap_or_default(),
            Err(e) => {
                tracing::error!(error = %e, "backend call failed");
                return error(request.id, ErrorCode::Unavailable, "services unavailable");
            }
        };
        if let Some(response::Body::Connect(c)) = &response.body {
            let user_id = c.user.as_ref().map(|u| u.user_id).unwrap_or_default();
            let device_id = c.device_id;
            {
                let mut guard = self.identity.lock().unwrap();
                if guard.connected {
                    return error(
                        request.id,
                        ErrorCode::FailedPrecondition,
                        "already connected",
                    );
                }
                *guard = Identity {
                    user_id,
                    device_id,
                    connected: true,
                };
            }
            self.app
                .push
                .attach(user_id, device_id, self.out.clone())
                .await;
        }
        response
    }

    async fn closed(&self) {
        let id = self.identity();
        if id.connected {
            self.app
                .push
                .detach(id.user_id, id.device_id, &self.out)
                .await;
        }
    }

    async fn send_frame(&self, frame: Frame) {
        self.send(Bytes::from(frame.encode_to_vec())).await;
    }

    /// Queues bytes for the writer. A device that does not keep up is
    /// disconnected; it syncs on reconnect.
    async fn send(&self, data: Bytes) {
        if self.out.try_send(data).is_err() {
            tracing::warn!("outbound queue full, closing");
            self.close(1013, "too slow").await;
        }
    }

    async fn close(&self, code: u16, reason: &str) {
        // The writer closes the sink once the queue drops; a close frame
        // with the reason is best effort.
        let _ = CloseFrame {
            code,
            reason: reason.into(),
        };
    }
}

fn error(id: u64, code: ErrorCode, message: &str) -> WireResponse {
    WireResponse {
        request_id: id,
        body: Some(response::Body::Error(pb::Error {
            code: code as i32,
            message: message.to_string(),
        })),
    }
}

fn request_type(request: &Request) -> &'static str {
    match request.body {
        Some(request::Body::Connect(_)) => "connect",
        Some(request::Body::OpenConversation(_)) => "open_conversation",
        Some(request::Body::SendMessage(_)) => "send_message",
        Some(request::Body::MarkRead(_)) => "mark_read",
        Some(request::Body::Sync(_)) => "sync",
        Some(request::Body::ListConversations(_)) => "list_conversations",
        Some(request::Body::GetMessages(_)) => "get_messages",
        None => "unknown",
    }
}

/// Wraps an already serialized Event in a Frame without decoding it:
/// Frame.event is field 3.
pub fn event_frame(payload: &[u8]) -> Bytes {
    let mut out = Vec::with_capacity(payload.len() + 8);
    out.push(0x1a);
    prost::encoding::encode_varint(payload.len() as u64, &mut out);
    out.extend_from_slice(payload);
    Bytes::from(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pb::v1::{Event, ReadEvent, event};

    #[test]
    fn event_frame_wraps_without_decoding() {
        for len in [1usize, 200, 70_000] {
            let event = Event {
                cursor: len as u64,
                occurred_at: None,
                body: Some(event::Body::MessageFailed(pb::MessageFailedEvent {
                    conversation_id: 7,
                    client_message_id: "x".repeat(len),
                    reason: String::new(),
                })),
            };
            let frame = Frame::decode(event_frame(&event.encode_to_vec())).unwrap();
            assert_eq!(frame.kind, Some(frame::Kind::Event(event)));
        }
        let read = Event {
            cursor: 1,
            occurred_at: None,
            body: Some(event::Body::Read(ReadEvent {
                conversation_id: 1,
                user_id: 2,
                seq: 3,
            })),
        };
        assert_eq!(
            Frame::decode(event_frame(&read.encode_to_vec()))
                .unwrap()
                .kind,
            Some(frame::Kind::Event(read))
        );
    }
}
