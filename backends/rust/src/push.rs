//! Pushes from fan-out workers through Valkey: one channel per user,
//! subscribed while this node holds one of the user's connections, delivered
//! to every connection of that user.

use std::collections::{HashMap, HashSet};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use bytes::Bytes;
use futures_util::StreamExt;
use redis::Msg;
use redis::aio::{PubSub, PubSubSink};
use tokio::sync::{mpsc, oneshot};

use crate::conn::Link;
use crate::metrics::Metrics;

const CHANNEL_PREFIX: &str = "push:";
/// How long a SUBSCRIBE or UNSUBSCRIBE may take before the connection is
/// given up and made again.
const COMMAND_TIMEOUT: Duration = Duration::from_secs(5);
/// How long an attach waits for its user's channel to be subscribed.
const SUBSCRIBE_WAIT: Duration = Duration::from_secs(5);
const MIN_BACKOFF: Duration = Duration::from_millis(100);
const MAX_BACKOFF: Duration = Duration::from_secs(5);

type Outbound = Arc<Link>;
/// The users with a connection here, which is the set of channels this
/// node should be subscribed to.
type Registry = Arc<Mutex<HashMap<u64, Vec<Outbound>>>>;
type Waiters = HashMap<u64, Vec<oneshot::Sender<()>>>;

/// Asks the pub/sub task to bring a user's subscription in line with the
/// registry, and to tell `subscribed` once the channel is subscribed.
struct Reconcile {
    user_id: u64,
    subscribed: Option<oneshot::Sender<()>>,
}

pub struct Push {
    registry: Registry,
    requests: mpsc::UnboundedSender<Reconcile>,
}

impl Push {
    /// Starts the pub/sub task, which connects, and reconnects, on its own.
    pub fn start(client: redis::Client, metrics: Arc<Metrics>) -> Self {
        let registry = Registry::default();
        let (requests, receiver) = mpsc::unbounded_channel();
        tokio::spawn(run(client, receiver, registry.clone(), metrics));
        Self { registry, requests }
    }

    /// Delivers the user's pushes to `out`; true once the user's channel is
    /// subscribed, false if that did not happen in time.
    pub async fn attach(&self, user_id: u64, out: Outbound) -> bool {
        self.registry
            .lock()
            .unwrap()
            .entry(user_id)
            .or_default()
            .push(out);
        let (subscribed, done) = oneshot::channel();
        let request = Reconcile {
            user_id,
            subscribed: Some(subscribed),
        };
        if self.requests.send(request).is_err() {
            return false;
        }
        matches!(tokio::time::timeout(SUBSCRIBE_WAIT, done).await, Ok(Ok(())))
    }

    /// Stops delivering to `out`, unsubscribing after the user's last.
    pub fn detach(&self, user_id: u64, out: &Outbound) {
        {
            let mut registry = self.registry.lock().unwrap();
            let Some(conns) = registry.get_mut(&user_id) else {
                return;
            };
            conns.retain(|c| !Arc::ptr_eq(c, out));
            if conns.is_empty() {
                registry.remove(&user_id);
            }
        }
        let _ = self.requests.send(Reconcile {
            user_id,
            subscribed: None,
        });
    }
}

/// Why a pub/sub session ended.
enum End {
    Shutdown,
    Lost,
}

/// Owns the pub/sub connection: applies each user's desired subscription in
/// turn, delivers messages, and on a lost connection makes a new one with
/// backoff and subscribes every channel the registry still wants.
async fn run(
    client: redis::Client,
    mut requests: mpsc::UnboundedReceiver<Reconcile>,
    registry: Registry,
    metrics: Arc<Metrics>,
) {
    let mut waiters = Waiters::new();
    let mut backoff = MIN_BACKOFF;
    let mut first = true;
    loop {
        if !first && !pause(backoff, &mut requests, &mut waiters).await {
            return;
        }
        first = false;
        let pubsub = match tokio::time::timeout(COMMAND_TIMEOUT, client.get_async_pubsub()).await {
            Ok(Ok(pubsub)) => pubsub,
            Ok(Err(e)) => {
                tracing::warn!(error = %e, ?backoff, "pub/sub connect failed");
                backoff = (backoff * 2).min(MAX_BACKOFF);
                continue;
            }
            Err(_) => {
                tracing::warn!(?backoff, "pub/sub connect timed out");
                backoff = (backoff * 2).min(MAX_BACKOFF);
                continue;
            }
        };
        backoff = MIN_BACKOFF;
        match session(pubsub, &mut requests, &registry, &metrics, &mut waiters).await {
            End::Shutdown => return,
            End::Lost => tracing::warn!("pub/sub connection lost, reconnecting"),
        }
    }
}

/// Waits out a backoff, keeping the waiters of requests that arrive.
/// False once the gateway is shutting down.
async fn pause(
    backoff: Duration,
    requests: &mut mpsc::UnboundedReceiver<Reconcile>,
    waiters: &mut Waiters,
) -> bool {
    let sleep = tokio::time::sleep(backoff);
    tokio::pin!(sleep);
    loop {
        tokio::select! {
            () = &mut sleep => return true,
            request = requests.recv() => match request {
                Some(request) => wait_for(waiters, request),
                None => return false,
            },
        }
    }
}

async fn session(
    pubsub: PubSub,
    requests: &mut mpsc::UnboundedReceiver<Reconcile>,
    registry: &Registry,
    metrics: &Metrics,
    waiters: &mut Waiters,
) -> End {
    let (mut sink, mut stream) = pubsub.split();
    let mut applied = HashSet::new();
    let wanted: HashSet<u64> = registry
        .lock()
        .unwrap()
        .keys()
        .chain(waiters.keys())
        .copied()
        .collect();
    if !wanted.is_empty() {
        tracing::info!(channels = wanted.len(), "resubscribing");
    }
    for user_id in wanted {
        if let Err(e) = reconcile(&mut sink, &mut applied, waiters, registry, user_id).await {
            tracing::warn!(error = %e, "resubscribe failed");
            return End::Lost;
        }
    }
    loop {
        tokio::select! {
            request = requests.recv() => {
                let Some(request) = request else {
                    return End::Shutdown;
                };
                let user_id = request.user_id;
                wait_for(waiters, request);
                if let Err(e) = reconcile(&mut sink, &mut applied, waiters, registry, user_id).await {
                    tracing::warn!(error = %e, "subscription change failed");
                    return End::Lost;
                }
            }
            message = stream.next() => match message {
                Some(message) => deliver(registry, metrics, &message),
                None => return End::Lost,
            },
        }
    }
}

fn wait_for(waiters: &mut Waiters, request: Reconcile) {
    if let Some(subscribed) = request.subscribed {
        waiters.entry(request.user_id).or_default().push(subscribed);
    }
}

/// Subscribes or unsubscribes the user's channel to match whether the
/// registry has a connection of theirs, then answers the user's waiters.
async fn reconcile(
    sink: &mut PubSubSink,
    applied: &mut HashSet<u64>,
    waiters: &mut Waiters,
    registry: &Registry,
    user_id: u64,
) -> redis::RedisResult<()> {
    let wanted = registry.lock().unwrap().contains_key(&user_id);
    if wanted != applied.contains(&user_id) {
        let command = async {
            if wanted {
                sink.subscribe(channel(user_id)).await
            } else {
                sink.unsubscribe(channel(user_id)).await
            }
        };
        match tokio::time::timeout(COMMAND_TIMEOUT, command).await {
            Ok(result) => result?,
            Err(_) => {
                return Err(redis::RedisError::from((
                    redis::ErrorKind::Io,
                    "pub/sub command timed out",
                )));
            }
        }
        if wanted {
            applied.insert(user_id);
        } else {
            applied.remove(&user_id);
        }
    }
    // Waiters whose connection has gone meanwhile are dropped unanswered.
    for subscribed in waiters.remove(&user_id).into_iter().flatten() {
        if wanted {
            let _ = subscribed.send(());
        }
    }
    Ok(())
}

fn deliver(registry: &Registry, metrics: &Metrics, message: &Msg) {
    let Some(user_id) = message
        .get_channel_name()
        .strip_prefix(CHANNEL_PREFIX)
        .and_then(|id| id.parse::<u64>().ok())
    else {
        return;
    };
    let frame = event_frame(message.get_payload_bytes());
    let targets = registry
        .lock()
        .unwrap()
        .get(&user_id)
        .cloned()
        .unwrap_or_default();
    for out in targets {
        // A device that does not keep up is disconnected; it syncs on reconnect.
        if out.push(frame.clone()) {
            metrics.pushed();
        }
    }
}

fn channel(user_id: u64) -> String {
    format!("{CHANNEL_PREFIX}{user_id}")
}

/// Wraps an already serialized Event in a Frame without decoding it:
/// Frame.event is field 3.
fn event_frame(payload: &[u8]) -> Bytes {
    let mut out = Vec::with_capacity(payload.len() + 8);
    out.push(0x1a);
    prost::encoding::encode_varint(payload.len() as u64, &mut out);
    out.extend_from_slice(payload);
    Bytes::from(out)
}

#[cfg(test)]
mod tests {
    use prost::Message;

    use super::*;
    use crate::pb::v1::{Event, Frame, MessageFailedEvent, ReadEvent, event, frame};

    fn wrapped(event: &Event) -> Option<frame::Kind> {
        Frame::decode(event_frame(&event.encode_to_vec()))
            .unwrap()
            .kind
    }

    #[test]
    fn event_frame_wraps_without_decoding() {
        // Lengths on both sides of the one- and two-byte varint boundaries.
        for len in [1usize, 200, 70_000] {
            let event = Event {
                cursor: len as u64,
                occurred_at: None,
                body: Some(event::Body::MessageFailed(MessageFailedEvent {
                    conversation_id: 7,
                    client_message_id: "x".repeat(len),
                    reason: String::new(),
                })),
            };
            assert_eq!(wrapped(&event), Some(frame::Kind::Event(event)));
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
        assert_eq!(wrapped(&read), Some(frame::Kind::Event(read)));
    }
}
