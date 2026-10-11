//! Pushes from fan-out workers through Valkey: one channel per user,
//! subscribed while this node holds one of the user's connections, delivered
//! to every connection of that user.

use std::collections::HashMap;
use std::sync::Arc;

use bytes::Bytes;
use futures_util::StreamExt;
use redis::Msg;
use redis::aio::PubSub;
use tokio::sync::{Mutex, mpsc};

const CHANNEL_PREFIX: &str = "push:";

type Outbound = mpsc::Sender<Bytes>;
type Registry = Arc<Mutex<HashMap<u64, Vec<Outbound>>>>;

enum Command {
    Subscribe(u64),
    Unsubscribe(u64),
}

pub struct Push {
    registry: Registry,
    commands: mpsc::UnboundedSender<Command>,
}

impl Push {
    pub async fn connect(client: &redis::Client) -> redis::RedisResult<Self> {
        let pubsub = client.get_async_pubsub().await?;
        let registry = Registry::default();
        let (commands, receiver) = mpsc::unbounded_channel();
        tokio::spawn(run(pubsub, receiver, registry.clone()));
        Ok(Self { registry, commands })
    }

    /// Delivers the user's pushes to `out`, subscribing on the first.
    pub async fn attach(&self, user_id: u64, out: Outbound) {
        let first = {
            let mut registry = self.registry.lock().await;
            let conns = registry.entry(user_id).or_default();
            conns.push(out);
            conns.len() == 1
        };
        if first {
            let _ = self.commands.send(Command::Subscribe(user_id));
        }
    }

    /// Stops delivering to `out`, unsubscribing after the last.
    pub async fn detach(&self, user_id: u64, out: &Outbound) {
        let last = {
            let mut registry = self.registry.lock().await;
            let Some(conns) = registry.get_mut(&user_id) else {
                return;
            };
            conns.retain(|c| !c.same_channel(out));
            let last = conns.is_empty();
            if last {
                registry.remove(&user_id);
            }
            last
        };
        if last {
            let _ = self.commands.send(Command::Unsubscribe(user_id));
        }
    }
}

async fn run(pubsub: PubSub, mut commands: mpsc::UnboundedReceiver<Command>, registry: Registry) {
    let (mut sink, mut stream) = pubsub.split();
    loop {
        tokio::select! {
            command = commands.recv() => match command {
                Some(Command::Subscribe(user_id)) => {
                    if let Err(e) = sink.subscribe(channel(user_id)).await {
                        tracing::warn!(error = %e, "subscribe failed");
                    }
                }
                Some(Command::Unsubscribe(user_id)) => {
                    let _ = sink.unsubscribe(channel(user_id)).await;
                }
                None => return,
            },
            message = stream.next() => {
                if let Some(message) = message {
                    deliver(&registry, &message).await;
                }
            }
        }
    }
}

async fn deliver(registry: &Registry, message: &Msg) {
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
        .await
        .get(&user_id)
        .cloned()
        .unwrap_or_default();
    for out in targets {
        // A device that does not keep up misses the push; it gets the event on its next sync.
        let _ = out.try_send(frame.clone());
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
