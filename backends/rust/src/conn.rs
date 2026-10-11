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

use crate::pb::backend::v1::{AckRequest, CallRequest};
use crate::pb::v1::{
    self as pb, ErrorCode, Frame, Request, Response as WireResponse, frame, request, response,
};
use crate::{App, BackendClient};

const MAX_FRAME_BYTES: usize = 64 * 1024;
const OUTBOUND_QUEUE: usize = 32;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);

/// The device a connection belongs to, known once Connect succeeds.
#[derive(Clone, Copy, Default)]
struct Device {
    user_id: u64,
    device_id: u64,
}

pub struct Conn {
    app: Arc<App>,
    out: mpsc::Sender<Bytes>,
    device: Mutex<Option<Device>>,
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
        device: Mutex::new(None),
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
    conn.detach().await;
    drop(conn);
    let _ = writer.await;
    app.metrics.connection(-1);
}

impl Conn {
    fn device(&self) -> Option<Device> {
        *self.device.lock().unwrap()
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
                if let Some(device) = self.device() {
                    tokio::spawn(ack_cursor(self.app.backend.clone(), device, ack.cursor));
                }
            }
            Some(frame::Kind::Ping(_)) => {
                self.send_frame(frame::Kind::Pong(pb::Pong {})).await;
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
        let kind = request.body.as_ref().map_or("unknown", request::Body::name);
        let response = tokio::time::timeout(REQUEST_TIMEOUT, self.dispatch(request))
            .await
            .unwrap_or_else(|_| services_unavailable(0));
        let code = match &response.body {
            Some(response::Body::Error(e)) => e.code().as_str_name(),
            _ => "ok",
        };
        self.send_frame(frame::Kind::Response(response)).await;
        self.app.metrics.request(kind, code, started.elapsed());
    }

    /// Applies the connection's state rules, then forwards to the services.
    async fn dispatch(&self, request: Request) -> WireResponse {
        let request_id = request.id;
        let device = self.device();
        let is_connect = matches!(request.body, Some(request::Body::Connect(_)));
        match (is_connect, device) {
            (true, Some(_)) => return already_connected(request_id),
            (false, None) => return error(request_id, ErrorCode::Unauthenticated, "connect first"),
            _ => {}
        }
        let device = device.unwrap_or_default();
        let call = CallRequest {
            user_id: device.user_id,
            device_id: device.device_id,
            request: Some(request),
        };
        let mut backend = self.app.backend.clone();
        let response = match backend.call(call).await {
            Ok(r) => r.into_inner().response.unwrap_or_default(),
            Err(e) => {
                tracing::error!(error = %e, "backend call failed");
                return services_unavailable(request_id);
            }
        };
        if let Some(response::Body::Connect(c)) = &response.body {
            let device = Device {
                user_id: c.user.as_ref().map(|u| u.user_id).unwrap_or_default(),
                device_id: c.device_id,
            };
            if !self.attach(device).await {
                return already_connected(request_id);
            }
        }
        response
    }

    /// Binds the connection to its device; false if a concurrent Connect
    /// already did.
    async fn attach(&self, device: Device) -> bool {
        {
            let mut current = self.device.lock().unwrap();
            if current.is_some() {
                return false;
            }
            *current = Some(device);
        }
        self.app.push.attach(device.user_id, self.out.clone()).await;
        self.app
            .presence
            .set(device.user_id, device.device_id)
            .await;
        true
    }

    async fn detach(&self) {
        if let Some(device) = self.device() {
            self.app.push.detach(device.user_id, &self.out).await;
            self.app
                .presence
                .remove(device.user_id, device.device_id)
                .await;
        }
    }

    async fn send_frame(&self, kind: frame::Kind) {
        let frame = Frame { kind: Some(kind) };
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

async fn ack_cursor(mut backend: BackendClient, device: Device, cursor: u64) {
    let ack = AckRequest {
        user_id: device.user_id,
        device_id: device.device_id,
        cursor,
    };
    if let Err(e) = backend.ack(ack).await {
        tracing::warn!(error = %e, "ack failed");
    }
}

fn error(request_id: u64, code: ErrorCode, message: &str) -> WireResponse {
    WireResponse {
        request_id,
        body: Some(response::Body::Error(pb::Error {
            code: code as i32,
            message: message.to_string(),
        })),
    }
}

fn already_connected(request_id: u64) -> WireResponse {
    error(
        request_id,
        ErrorCode::FailedPrecondition,
        "already connected",
    )
}

fn services_unavailable(request_id: u64) -> WireResponse {
    error(request_id, ErrorCode::Unavailable, "services unavailable")
}

impl request::Body {
    /// The request's name in metrics, as the other backends label it.
    fn name(&self) -> &'static str {
        match self {
            Self::Connect(_) => "connect",
            Self::OpenConversation(_) => "open_conversation",
            Self::SendMessage(_) => "send_message",
            Self::MarkRead(_) => "mark_read",
            Self::Sync(_) => "sync",
            Self::ListConversations(_) => "list_conversations",
            Self::GetMessages(_) => "get_messages",
            Self::RegisterPushToken(_) => "register_push_token",
        }
    }
}
