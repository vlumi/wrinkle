//! One device's WebSocket connection: a read loop on the handler's task, a
//! writer task over a bounded queue, and a task per request, within the
//! contract's limits. A gateway-wide sweep pings and closes idle connections.

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::extract::State;
use axum::extract::ws::{CloseFrame, Message, WebSocket, WebSocketUpgrade};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use bytes::Bytes;
use futures_util::stream::SplitSink;
use futures_util::{SinkExt, StreamExt};
use prost::Message as _;
use tokio::sync::mpsc::error::TrySendError;
use tokio::sync::{Notify, mpsc, oneshot};

use crate::pb::backend::v1::{AckRequest, CallRequest};
use crate::pb::v1::{
    self as pb, ErrorCode, Frame, Request, Response as WireResponse, frame, request, response,
};
use crate::user_id::UserId;
use crate::{App, BackendClient};

const MAX_FRAME_BYTES: usize = 64 * 1024;
const OUTBOUND_QUEUE: usize = 32;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(5);
const MAX_IN_FLIGHT: usize = 32;
const BURST: f64 = 100.0;
const REFILL_PER_SECOND: f64 = 50.0;
const PING_INTERVAL: Duration = Duration::from_secs(30);
const IDLE_TIMEOUT: Duration = Duration::from_secs(90);
const SWEEP_INTERVAL: Duration = Duration::from_secs(10);
/// How long a close frame may take to write before the socket is dropped.
const CLOSE_WRITE_TIMEOUT: Duration = Duration::from_secs(3);

const GOING_AWAY: u16 = 1001;
const UNSUPPORTED_DATA: u16 = 1003;
const POLICY_VIOLATION: u16 = 1008;
const MESSAGE_TOO_BIG: u16 = 1009;
const INTERNAL_ERROR: u16 = 1011;
const TRY_AGAIN_LATER: u16 = 1013;
const SIGNED_OUT: u16 = 4001;

/// What the writer sends.
enum Out {
    Frame(Bytes),
    Ping,
}

struct Close {
    code: u16,
    reason: &'static str,
}

/// The parts of a connection other tasks reach: the outbound queue, the
/// signal that closes it, and when it last received anything.
pub struct Link {
    id: u64,
    queue: mpsc::Sender<Out>,
    close: Mutex<Option<oneshot::Sender<Close>>>,
    /// Seconds since the gateway's epoch.
    last_seen: AtomicU32,
}

impl Link {
    /// Queues a push; true if queued. A device that does not keep up is
    /// disconnected; it syncs on reconnect.
    pub fn push(&self, frame: Bytes) -> bool {
        match self.queue.try_send(Out::Frame(frame)) {
            Ok(()) => true,
            Err(TrySendError::Full(_)) => {
                if self.close(TRY_AGAIN_LATER, "too slow") {
                    tracing::warn!("outbound queue full, closing");
                }
                false
            }
            Err(TrySendError::Closed(_)) => false,
        }
    }

    /// Queues what the device asked for, waiting for room, which holds back
    /// the device's own requests; false once the writer is gone.
    async fn respond(&self, frame: Bytes) -> bool {
        self.queue.send(Out::Frame(frame)).await.is_ok()
    }

    /// Closes the connection of a device signed out.
    pub fn sign_out(&self) {
        self.close(SIGNED_OUT, "signed out");
    }

    /// Has the writer send a close frame and stop; the first close wins and
    /// returns true.
    fn close(&self, code: u16, reason: &'static str) -> bool {
        let Some(close) = self.close.lock().unwrap().take() else {
            return false;
        };
        let _ = close.send(Close { code, reason });
        true
    }

    /// Stops the writer without a close frame, unless one is already sent.
    fn finish(&self) {
        drop(self.close.lock().unwrap().take());
    }
}

/// Every connection on this gateway, for the keepalive sweep and the drain.
pub struct Conns {
    epoch: Instant,
    next_id: AtomicU64,
    all: Mutex<HashMap<u64, Arc<Link>>>,
    in_flight: AtomicUsize,
    draining: AtomicBool,
}

impl Default for Conns {
    fn default() -> Self {
        Self {
            epoch: Instant::now(),
            next_id: AtomicU64::default(),
            all: Mutex::default(),
            in_flight: AtomicUsize::default(),
            draining: AtomicBool::default(),
        }
    }
}

impl Conns {
    fn now(&self) -> u32 {
        self.epoch.elapsed().as_secs() as u32
    }

    fn register(&self) -> (Arc<Link>, mpsc::Receiver<Out>, oneshot::Receiver<Close>) {
        let (queue, outbound) = mpsc::channel(OUTBOUND_QUEUE);
        let (close, closed) = oneshot::channel();
        let link = Arc::new(Link {
            id: self.next_id.fetch_add(1, Ordering::Relaxed),
            queue,
            close: Mutex::new(Some(close)),
            last_seen: AtomicU32::new(self.now()),
        });
        self.all.lock().unwrap().insert(link.id, link.clone());
        if self.draining() {
            link.close(GOING_AWAY, "shutting down");
        }
        (link, outbound, closed)
    }

    fn deregister(&self, link: &Link) {
        self.all.lock().unwrap().remove(&link.id);
    }

    pub fn draining(&self) -> bool {
        self.draining.load(Ordering::SeqCst)
    }

    /// Refuses new connections and closes every open one.
    pub fn drain(&self) {
        self.draining.store(true, Ordering::SeqCst);
        for link in self.all.lock().unwrap().values() {
            link.close(GOING_AWAY, "shutting down");
        }
    }

    /// True once every connection has detached and no request is running.
    pub fn settled(&self) -> bool {
        self.all.lock().unwrap().is_empty() && self.in_flight.load(Ordering::SeqCst) == 0
    }

    /// Pings every connection now and then and closes the idle ones.
    pub async fn keepalive(&self) {
        let pings_every = (PING_INTERVAL.as_secs() / SWEEP_INTERVAL.as_secs()).max(1);
        let mut tick = tokio::time::interval(SWEEP_INTERVAL);
        tick.tick().await;
        let mut sweeps = 0u64;
        loop {
            tick.tick().await;
            sweeps += 1;
            self.sweep(sweeps.is_multiple_of(pings_every));
        }
    }

    fn sweep(&self, ping: bool) {
        let now = self.now();
        let idle = IDLE_TIMEOUT.as_secs() as u32;
        for link in self.all.lock().unwrap().values() {
            if now.saturating_sub(link.last_seen.load(Ordering::Relaxed)) >= idle {
                link.close(GOING_AWAY, "idle");
            } else if ping {
                // A full queue already shows the device is busy receiving.
                let _ = link.queue.try_send(Out::Ping);
            }
        }
    }
}

/// The device a connection belongs to, known once Connect succeeds.
#[derive(Clone, Copy, Default)]
struct Device {
    user_id: UserId,
    device_id: u64,
}

enum Attach {
    Bound,
    /// Another Connect bound the connection first, or it has ended.
    Taken,
    /// The user's push channel could not be subscribed in time.
    Unsubscribed,
}

enum Binding {
    Pending,
    Bound(Device),
    Gone,
}

pub struct Conn {
    app: Arc<App>,
    link: Arc<Link>,
    binding: Mutex<Binding>,
    /// Signaled when a Connect is undone, or a Register or SignIn answered,
    /// so the Connect that follows gets a fresh deadline.
    restart_deadline: Notify,
    in_flight: AtomicUsize,
    acks: Mutex<Acks>,
}

pub async fn upgrade(ws: WebSocketUpgrade, State(app): State<Arc<App>>) -> Response {
    if app.conns.draining() {
        return (StatusCode::SERVICE_UNAVAILABLE, "shutting down\n").into_response();
    }
    ws.max_message_size(MAX_FRAME_BYTES)
        .max_frame_size(MAX_FRAME_BYTES)
        .read_buffer_size(1024)
        .write_buffer_size(0)
        .on_upgrade(move |socket| serve(socket, app))
}

async fn serve(socket: WebSocket, app: Arc<App>) {
    app.metrics.connection(1);
    let (link, outbound, closed) = app.conns.register();
    let (sink, mut stream) = socket.split();
    let mut writer = tokio::spawn(write(sink, outbound, closed));
    let conn = Arc::new(Conn {
        app: app.clone(),
        link: link.clone(),
        binding: Mutex::new(Binding::Pending),
        restart_deadline: Notify::new(),
        in_flight: AtomicUsize::default(),
        acks: Mutex::default(),
    });

    let mut bucket = TokenBucket::new(Instant::now());
    let connect_deadline = tokio::time::sleep(CONNECT_TIMEOUT);
    tokio::pin!(connect_deadline);
    let mut awaiting_connect = true;
    let mut writer_done = false;
    loop {
        tokio::select! {
            message = stream.next() => {
                link.last_seen.store(app.conns.now(), Ordering::Relaxed);
                match message {
                    Some(Ok(Message::Binary(data))) => {
                        if !conn.receive(data, &mut bucket).await {
                            break;
                        }
                    }
                    Some(Ok(Message::Text(_))) => {
                        link.close(UNSUPPORTED_DATA, "frames are binary protobuf");
                        break;
                    }
                    Some(Ok(Message::Ping(_) | Message::Pong(_))) => {}
                    Some(Ok(Message::Close(_))) | None => break,
                    Some(Err(e)) => {
                        if too_big(&e) {
                            link.close(MESSAGE_TOO_BIG, "frame too large");
                        }
                        break;
                    }
                }
            }
            () = &mut connect_deadline, if awaiting_connect => {
                awaiting_connect = false;
                if conn.device().is_none() {
                    link.close(POLICY_VIOLATION, "connect first");
                    break;
                }
            }
            () = conn.restart_deadline.notified() => {
                connect_deadline
                    .as_mut()
                    .reset(tokio::time::Instant::now() + CONNECT_TIMEOUT);
                awaiting_connect = true;
            }
            _ = &mut writer => {
                writer_done = true;
                break;
            }
        }
    }
    link.finish();
    conn.detach().await;
    app.conns.deregister(&link);
    if !writer_done {
        let _ = writer.await;
    }
    app.metrics.connection(-1);
}

/// Writes the queue to the socket until the connection is closed or a write
/// fails, then sends the close frame if there is one.
async fn write(
    mut sink: SplitSink<WebSocket, Message>,
    mut outbound: mpsc::Receiver<Out>,
    closed: oneshot::Receiver<Close>,
) {
    let close = {
        let pump = async {
            while let Some(out) = outbound.recv().await {
                let message = match out {
                    Out::Frame(data) => Message::Binary(data),
                    Out::Ping => Message::Ping(Bytes::new()),
                };
                if let Err(e) = sink.send(message).await {
                    tracing::warn!(error = %e, "socket write failed");
                    return Some(Close {
                        code: INTERNAL_ERROR,
                        reason: "write failed",
                    });
                }
            }
            None
        };
        tokio::select! {
            biased;
            close = closed => close.ok(),
            close = pump => close,
        }
    };
    if let Some(Close { code, reason }) = close {
        let frame = CloseFrame {
            code,
            reason: reason.into(),
        };
        let _ =
            tokio::time::timeout(CLOSE_WRITE_TIMEOUT, sink.send(Message::Close(Some(frame)))).await;
    }
}

/// True for a read error from a frame over the size limit.
fn too_big(e: &axum::Error) -> bool {
    let mut source = std::error::Error::source(e);
    while let Some(e) = source {
        if let Some(tungstenite::Error::Capacity(_)) = e.downcast_ref() {
            return true;
        }
        source = e.source();
    }
    false
}

impl Conn {
    fn device(&self) -> Option<Device> {
        match *self.binding.lock().unwrap() {
            Binding::Bound(device) => Some(device),
            _ => None,
        }
    }

    /// Handles one frame; false ends the connection.
    async fn receive(self: &Arc<Self>, data: Bytes, bucket: &mut TokenBucket) -> bool {
        let Ok(frame) = Frame::decode(data) else {
            self.link.close(POLICY_VIOLATION, "malformed frame");
            return false;
        };
        let allowed = bucket.take(Instant::now());
        match frame.kind {
            Some(frame::Kind::Request(request)) => {
                if !allowed {
                    return self.reject(&request, "rate limit exceeded").await;
                }
                if self.in_flight.fetch_add(1, Ordering::SeqCst) >= MAX_IN_FLIGHT {
                    self.in_flight.fetch_sub(1, Ordering::SeqCst);
                    return self.reject(&request, "too many requests in flight").await;
                }
                self.app.conns.in_flight.fetch_add(1, Ordering::SeqCst);
                let conn = self.clone();
                tokio::spawn(async move { conn.handle_request(request).await });
            }
            Some(frame::Kind::Ack(ack)) if allowed => self.ack(ack.cursor),
            Some(frame::Kind::Ping(_)) if allowed => {
                return self.respond(frame::Kind::Pong(pb::Pong {})).await;
            }
            Some(frame::Kind::Ack(_) | frame::Kind::Ping(_) | frame::Kind::Pong(_)) => {}
            _ => {
                self.link.close(POLICY_VIOLATION, "unexpected frame");
                return false;
            }
        }
        true
    }

    async fn reject(&self, request: &Request, message: &str) -> bool {
        let response = error(request.id, ErrorCode::ResourceExhausted, message);
        self.reply(request_name(request), Instant::now(), response)
            .await
    }

    async fn handle_request(self: Arc<Self>, request: Request) {
        let started = Instant::now();
        let kind = request_name(&request);
        let signs_in = matches!(
            request.body,
            Some(request::Body::Register(_) | request::Body::SignIn(_))
        );
        let response = self.dispatch(request).await;
        if signs_in {
            // Hashing the password may have taken a while; Connect gets a fresh deadline.
            self.restart_deadline.notify_one();
        }
        self.reply(kind, started, response).await;
        self.in_flight.fetch_sub(1, Ordering::SeqCst);
        self.app.conns.in_flight.fetch_sub(1, Ordering::SeqCst);
    }

    async fn reply(&self, kind: &str, started: Instant, response: WireResponse) -> bool {
        let code = match &response.body {
            Some(response::Body::Error(e)) => e.code().as_str_name(),
            _ => "ok",
        };
        self.app.metrics.request(kind, code, started.elapsed());
        self.respond(frame::Kind::Response(response)).await
    }

    /// Applies the connection's state rules, then forwards to the services.
    async fn dispatch(&self, request: Request) -> WireResponse {
        let request_id = request.id;
        let kind = request_name(&request);
        let device = self.device();
        let identifies = matches!(
            request.body,
            Some(request::Body::Register(_) | request::Body::SignIn(_) | request::Body::Connect(_))
        );
        match (identifies, device) {
            (true, Some(_)) => return already_connected(request_id),
            (false, None) => return error(request_id, ErrorCode::Unauthenticated, "connect first"),
            _ => {}
        }
        let call = CallRequest {
            user_id: device.map(|d| d.user_id.to_vec()).unwrap_or_default(),
            device_id: device.map_or(0, |d| d.device_id),
            request: Some(request),
        };
        let mut backend = self.app.backend.clone();
        let response = match tokio::time::timeout(REQUEST_TIMEOUT, backend.call(call)).await {
            Ok(Ok(r)) => r.into_inner().response.unwrap_or_default(),
            Ok(Err(e)) => {
                tracing::error!(error = %e, kind, "backend call failed");
                return services_unavailable(request_id);
            }
            Err(_) => {
                tracing::warn!(kind, "services call timed out");
                return services_unavailable(request_id);
            }
        };
        if let Some(response::Body::Connect(c)) = &response.body {
            let user_id = c.user.as_ref().map(|u| u.user_id.as_slice());
            let Some(Ok(user_id)) = user_id.map(UserId::try_from) else {
                return error(
                    request_id,
                    ErrorCode::Internal,
                    "connect returned no user id",
                );
            };
            let device = Device {
                user_id,
                device_id: c.device_id,
            };
            match self.attach(device).await {
                Attach::Bound => {}
                Attach::Taken => return already_connected(request_id),
                Attach::Unsubscribed => {
                    return error(
                        request_id,
                        ErrorCode::Unavailable,
                        "push channel unavailable",
                    );
                }
            }
        }
        response
    }

    /// Binds the connection to its device once its pushes are subscribed,
    /// unless a concurrent Connect already did or the connection has ended.
    async fn attach(&self, device: Device) -> Attach {
        {
            let mut binding = self.binding.lock().unwrap();
            if !matches!(*binding, Binding::Pending) {
                return Attach::Taken;
            }
            *binding = Binding::Bound(device);
        }
        if !self
            .app
            .push
            .attach(device.user_id, device.device_id, self.link.clone())
            .await
        {
            tracing::warn!("push subscription not ready, connect refused");
            let mut binding = self.binding.lock().unwrap();
            if matches!(*binding, Binding::Bound(_)) {
                *binding = Binding::Pending;
                self.restart_deadline.notify_one();
            }
            drop(binding);
            self.app.push.detach(device.user_id, &self.link);
            return Attach::Unsubscribed;
        }
        self.app
            .presence
            .set(device.user_id, device.device_id)
            .await;
        // The connection may have ended meanwhile, its detach finding nothing
        // yet to undo.
        if matches!(*self.binding.lock().unwrap(), Binding::Gone) {
            self.unbind(device).await;
        }
        Attach::Bound
    }

    async fn detach(&self) {
        let previous = std::mem::replace(&mut *self.binding.lock().unwrap(), Binding::Gone);
        if let Binding::Bound(device) = previous {
            self.unbind(device).await;
        }
    }

    async fn unbind(&self, device: Device) {
        self.app.push.detach(device.user_id, &self.link);
        self.app
            .presence
            .remove(device.user_id, device.device_id)
            .await;
    }

    /// Writes acks one at a time; acks arriving meanwhile collapse into the
    /// highest cursor, written next.
    fn ack(self: &Arc<Self>, cursor: u64) {
        let Some(device) = self.device() else {
            return;
        };
        let Some(first) = self.acks.lock().unwrap().offer(cursor) else {
            return;
        };
        let conn = self.clone();
        tokio::spawn(async move {
            let mut next = Some(first);
            while let Some(cursor) = next {
                ack_cursor(conn.app.backend.clone(), device, cursor).await;
                next = conn.acks.lock().unwrap().done();
            }
        });
    }

    async fn respond(&self, kind: frame::Kind) -> bool {
        let frame = Frame { kind: Some(kind) };
        self.link.respond(Bytes::from(frame.encode_to_vec())).await
    }
}

async fn ack_cursor(mut backend: BackendClient, device: Device, cursor: u64) {
    let ack = AckRequest {
        user_id: device.user_id.to_vec(),
        device_id: device.device_id,
        cursor,
    };
    match tokio::time::timeout(REQUEST_TIMEOUT, backend.ack(ack)).await {
        Ok(Ok(_)) => {}
        Ok(Err(e)) => tracing::warn!(error = %e, "ack failed"),
        Err(_) => tracing::warn!("ack timed out"),
    }
}

/// A connection's frame allowance: `BURST` at most, refilled continuously
/// at `REFILL_PER_SECOND`.
struct TokenBucket {
    tokens: f64,
    updated: Instant,
}

impl TokenBucket {
    fn new(now: Instant) -> Self {
        Self {
            tokens: BURST,
            updated: now,
        }
    }

    fn take(&mut self, now: Instant) -> bool {
        let elapsed = now.saturating_duration_since(self.updated).as_secs_f64();
        self.tokens = (self.tokens + elapsed * REFILL_PER_SECOND).min(BURST);
        self.updated = now;
        if self.tokens >= 1.0 {
            self.tokens -= 1.0;
            true
        } else {
            false
        }
    }
}

/// At most one ack write in flight, the highest cursor since kept for after.
#[derive(Default)]
struct Acks {
    writing: bool,
    pending: Option<u64>,
}

impl Acks {
    /// The cursor to write now, or None while a write is in flight.
    fn offer(&mut self, cursor: u64) -> Option<u64> {
        if self.writing {
            self.pending = self.pending.max(Some(cursor));
            None
        } else {
            self.writing = true;
            Some(cursor)
        }
    }

    /// After a write: the next cursor to write, or None once idle.
    fn done(&mut self) -> Option<u64> {
        let next = self.pending.take();
        self.writing = next.is_some();
        next
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

fn request_name(request: &Request) -> &'static str {
    request.body.as_ref().map_or("unknown", request::Body::name)
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
            Self::Register(_) => "register",
            Self::SignIn(_) => "sign_in",
            Self::ChangePassword(_) => "change_password",
            Self::UpdateProfile(_) => "update_profile",
            Self::SignOut(_) => "sign_out",
            Self::ListDevices(_) => "list_devices",
            Self::RenameDevice(_) => "rename_device",
            Self::SignOutDevice(_) => "sign_out_device",
            Self::DeleteAccount(_) => "delete_account",
            Self::CreateUpload(_) => "create_upload",
            Self::CreateGroup(_) => "create_group",
            Self::AddMembers(_) => "add_members",
            Self::RemoveMember(_) => "remove_member",
            Self::LeaveGroup(_) => "leave_group",
            Self::SetTopic(_) => "set_topic",
            Self::SetAdmin(_) => "set_admin",
            Self::GetMembers(_) => "get_members",
            Self::DeleteMessage(_) => "delete_message",
            Self::SetDeviceKey(_) => "set_device_key",
            Self::GetDeviceKeys(_) => "get_device_keys",
            Self::PublishKeys(_) => "publish_keys",
            Self::GetConversationKeys(_) => "get_conversation_keys",
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn token_bucket_allows_a_burst_then_the_refill_rate() {
        let start = Instant::now();
        let mut bucket = TokenBucket::new(start);
        for _ in 0..100 {
            assert!(bucket.take(start));
        }
        assert!(!bucket.take(start));

        // 50 a second: one token every 20 ms.
        let later = start + Duration::from_millis(100);
        let allowed = (0..10).filter(|_| bucket.take(later)).count();
        assert_eq!(allowed, 5);
    }

    #[test]
    fn token_bucket_refills_to_the_burst_only() {
        let start = Instant::now();
        let mut bucket = TokenBucket::new(start);
        assert!(bucket.take(start));
        let later = start + Duration::from_secs(60);
        let allowed = (0..200).filter(|_| bucket.take(later)).count();
        assert_eq!(allowed, 100);
    }

    #[test]
    fn acks_coalesce_to_the_highest_while_one_is_written() {
        let mut acks = Acks::default();
        assert_eq!(acks.offer(5), Some(5));
        assert_eq!(acks.offer(9), None);
        assert_eq!(acks.offer(7), None);
        assert_eq!(acks.done(), Some(9));
        assert_eq!(acks.done(), None);
        // Idle again: the next ack is written at once.
        assert_eq!(acks.offer(10), Some(10));
        assert_eq!(acks.done(), None);
    }
}
