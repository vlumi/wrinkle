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

use crate::ack::{self, Acks};
use crate::pb::backend::v1::{AckRequest, CallRequest};
use crate::pb::v1::{
    self as pb, ErrorCode, ErrorReason, Frame, Request, Response as WireResponse, frame, request,
    response,
};
use crate::user_id::UserId;
use crate::{App, BackendClient};

const MAX_FRAME_BYTES: usize = 64 * 1024;
const OUTBOUND_QUEUE: usize = 32;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(5);
/// Register and SignIn requests per connection, each of which may cost a
/// password hash; those refused as invalid, before hashing, do not count.
const MAX_SIGN_INS: usize = 5;
const MAX_IN_FLIGHT: usize = 32;
const BURST: f64 = 100.0;
const REFILL_PER_SECOND: f64 = 50.0;
const PING_INTERVAL: Duration = Duration::from_secs(30);
const IDLE_TIMEOUT: Duration = Duration::from_secs(90);
const SWEEP_INTERVAL: Duration = Duration::from_secs(10);
/// How long a frame may take to write before the connection is closed.
const WRITE_TIMEOUT: Duration = Duration::from_secs(10);
/// How long a close frame may take to write before the socket is dropped.
const CLOSE_WRITE_TIMEOUT: Duration = Duration::from_secs(3);

const NORMAL: u16 = 1000;
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
    /// Ack writer tasks, which may outlive their connections.
    ack_tasks: Arc<AtomicUsize>,
    ack_interval: Duration,
    draining: AtomicBool,
}

impl Conns {
    pub fn new(ack_interval: Duration) -> Self {
        Self {
            epoch: Instant::now(),
            next_id: AtomicU64::default(),
            all: Mutex::default(),
            in_flight: AtomicUsize::default(),
            ack_tasks: Arc::default(),
            ack_interval,
            draining: AtomicBool::default(),
        }
    }

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

    /// True once every connection has detached and no request or final ack
    /// is running.
    pub fn settled(&self) -> bool {
        self.all.lock().unwrap().is_empty()
            && self.in_flight.load(Ordering::SeqCst) == 0
            && self.ack_tasks.load(Ordering::SeqCst) == 0
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
#[derive(Clone, Copy, Default, PartialEq)]
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
    /// Signaled when a Connect is undone, or a Register or SignIn succeeds,
    /// so the Connect that follows gets a fresh deadline.
    restart_deadline: Notify,
    in_flight: AtomicUsize,
    sign_ins: AtomicUsize,
    /// Created on the first ack after Connect.
    acks: Mutex<Option<Arc<Acks<DeviceAcks>>>>,
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
        sign_ins: AtomicUsize::default(),
        acks: Mutex::default(),
    });
    // Its own task, so it fires while the read loop waits for room in the
    // queue.
    let deadline = tokio::spawn(conn.clone().connect_deadline());

    let mut bucket = TokenBucket::new(Instant::now());
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
                    Some(Ok(Message::Close(_))) => {
                        // Completes the closing handshake: the writer flushes
                        // the echo of the device's close frame.
                        link.close(NORMAL, "");
                        break;
                    }
                    None => break,
                    Some(Err(e)) => {
                        if too_big(&e) {
                            link.close(MESSAGE_TOO_BIG, "frame too large");
                        }
                        break;
                    }
                }
            }
            _ = &mut writer => {
                writer_done = true;
                break;
            }
        }
    }
    deadline.abort();
    link.finish();
    if let Some(acks) = conn.acks.lock().unwrap().take() {
        acks.close();
    }
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
                match tokio::time::timeout(WRITE_TIMEOUT, sink.send(message)).await {
                    Ok(Ok(())) => continue,
                    Ok(Err(e)) => tracing::warn!(error = %e, "socket write failed"),
                    Err(_) => tracing::warn!("socket write timed out"),
                }
                return Some(Close {
                    code: INTERNAL_ERROR,
                    reason: "write failed",
                });
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
    /// Closes the connection if it is not bound within `CONNECT_TIMEOUT` of
    /// opening or of the last restart; runs until aborted.
    async fn connect_deadline(self: Arc<Self>) {
        loop {
            tokio::select! {
                () = tokio::time::sleep(CONNECT_TIMEOUT) => {
                    if self.device().is_none() {
                        self.link.close(POLICY_VIOLATION, "connect first");
                        return;
                    }
                    self.restart_deadline.notified().await;
                }
                () = self.restart_deadline.notified() => {}
            }
        }
    }

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
            match &response.body {
                Some(response::Body::Error(e)) if e.code() == ErrorCode::InvalidArgument => {
                    self.sign_ins.fetch_sub(1, Ordering::SeqCst);
                }
                // A failed attempt keeps the deadline, so it cannot hold the
                // connection open.
                Some(response::Body::Error(_)) => {}
                // Hashing the password may have taken a while; Connect gets a
                // fresh deadline.
                _ => self.restart_deadline.notify_one(),
            }
        }
        self.reply(kind, started, response).await;
        self.in_flight.fetch_sub(1, Ordering::SeqCst);
        self.app.conns.in_flight.fetch_sub(1, Ordering::SeqCst);
    }

    async fn reply(&self, kind: &'static str, started: Instant, response: WireResponse) -> bool {
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
        let signs_in = matches!(
            request.body,
            Some(request::Body::Register(_) | request::Body::SignIn(_))
        );
        if signs_in && !self.count_sign_in() {
            return error(
                request_id,
                ErrorCode::ResourceExhausted,
                &format!("at most {MAX_SIGN_INS} sign-ins per connection"),
            );
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

    /// Counts a Register or SignIn; false beyond the connection's limit.
    fn count_sign_in(&self) -> bool {
        self.sign_ins
            .try_update(Ordering::SeqCst, Ordering::SeqCst, |n| {
                (n < MAX_SIGN_INS).then_some(n + 1)
            })
            .is_ok()
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

    /// Forwards the device's ack, at most once per interval.
    fn ack(&self, cursor: u64) {
        let Some(device) = self.device() else {
            return;
        };
        let mut acks = self.acks.lock().unwrap();
        // A Connect undone and redone may have bound another device.
        if let Some(old) = acks.take_if(|a| a.writer().device != device) {
            old.close();
        }
        let acks = acks.get_or_insert_with(|| {
            let writer = DeviceAcks {
                backend: self.app.backend.clone(),
                device,
            };
            let conns = &self.app.conns;
            Acks::new(conns.ack_interval, writer, conns.ack_tasks.clone())
        });
        acks.ack(cursor);
    }

    async fn respond(&self, kind: frame::Kind) -> bool {
        let frame = Frame { kind: Some(kind) };
        self.link.respond(Bytes::from(frame.encode_to_vec())).await
    }
}

struct DeviceAcks {
    backend: BackendClient,
    device: Device,
}

impl ack::Write for DeviceAcks {
    async fn write(&self, cursor: u64) {
        let ack = AckRequest {
            user_id: self.device.user_id.to_vec(),
            device_id: self.device.device_id,
            cursor,
        };
        let mut backend = self.backend.clone();
        match tokio::time::timeout(REQUEST_TIMEOUT, backend.ack(ack)).await {
            Ok(Ok(_)) => {}
            Ok(Err(e)) => tracing::warn!(error = %e, "ack failed"),
            Err(_) => tracing::warn!("ack timed out"),
        }
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

fn error(request_id: u64, code: ErrorCode, message: &str) -> WireResponse {
    error_because(request_id, code, ErrorReason::Unspecified, message)
}

/// An error with the reason clients act on.
fn error_because(
    request_id: u64,
    code: ErrorCode,
    reason: ErrorReason,
    message: &str,
) -> WireResponse {
    WireResponse {
        request_id,
        body: Some(response::Body::Error(pb::Error {
            code: code as i32,
            message: message.to_string(),
            reason: reason as i32,
        })),
    }
}

fn already_connected(request_id: u64) -> WireResponse {
    error_because(
        request_id,
        ErrorCode::FailedPrecondition,
        ErrorReason::AlreadyConnected,
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
            Self::SetReaction(_) => "set_reaction",
            Self::CreateSticker(_) => "create_sticker",
            Self::GetStickers(_) => "get_stickers",
            Self::GetPalette(_) => "get_palette",
            Self::SaveSticker(_) => "save_sticker",
            Self::RemoveSticker(_) => "remove_sticker",
            Self::MoveSticker(_) => "move_sticker",
            Self::ListContacts(_) => "list_contacts",
            Self::AddContact(_) => "add_contact",
            Self::RemoveContact(_) => "remove_contact",
            Self::BlockUser(_) => "block_user",
            Self::UnblockUser(_) => "unblock_user",
            Self::OpenNotes(_) => "open_notes",
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The 1009 close finds tungstenite's capacity error in what axum
    /// returns, which needs axum's tungstenite to be the one this crate
    /// names.
    #[tokio::test]
    async fn an_oversized_frame_reads_as_too_big() {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};

        let (tx, rx) = oneshot::channel();
        let tx = Arc::new(Mutex::new(Some(tx)));
        let handler = move |ws: WebSocketUpgrade| async move {
            ws.max_message_size(MAX_FRAME_BYTES)
                .max_frame_size(MAX_FRAME_BYTES)
                .on_upgrade(move |mut socket| async move {
                    let too_big = matches!(socket.recv().await, Some(Err(e)) if too_big(&e));
                    if let Some(tx) = tx.lock().unwrap().take() {
                        let _ = tx.send(too_big);
                    }
                })
        };
        let router = axum::Router::new().route("/", axum::routing::get(handler));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(listener, router).await });

        let mut client = tokio::net::TcpStream::connect(addr).await.unwrap();
        let upgrade = format!(
            "GET / HTTP/1.1\r\nHost: {addr}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\
             Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
        );
        client.write_all(upgrade.as_bytes()).await.unwrap();
        let mut head = Vec::new();
        while !head.ends_with(b"\r\n\r\n") {
            head.push(client.read_u8().await.unwrap());
        }
        assert!(head.starts_with(b"HTTP/1.1 101"));
        // A masked binary frame one byte over the limit.
        let mut frame = vec![0x82, 0x80 | 127];
        frame.extend_from_slice(&(MAX_FRAME_BYTES as u64 + 1).to_be_bytes());
        frame.extend_from_slice(&[0; 4]);
        frame.resize(frame.len() + MAX_FRAME_BYTES + 1, 0);
        let _ = client.write_all(&frame).await;

        let too_big = tokio::time::timeout(Duration::from_secs(5), rx).await;
        assert!(matches!(too_big, Ok(Ok(true))));
    }

    #[test]
    fn already_connected_names_its_reason() {
        let response = already_connected(7);
        assert_eq!(response.request_id, 7);
        let Some(response::Body::Error(e)) = response.body else {
            panic!("not an error: {response:?}");
        };
        assert_eq!(e.code(), ErrorCode::FailedPrecondition);
        assert_eq!(e.reason(), ErrorReason::AlreadyConnected);
    }

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
}
