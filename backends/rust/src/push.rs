//! Pushes from fan-out workers through Valkey: one channel per user,
//! subscribed while this node holds one of the user's connections, delivered
//! to every connection of that user. Also keeps the presence hash.

use std::collections::HashMap;
use std::sync::Arc;

use bytes::Bytes;
use futures_util::StreamExt;
use redis::AsyncCommands;
use tokio::sync::{Mutex, mpsc};

use crate::conn::event_frame;

type Registry = Arc<Mutex<HashMap<u64, Vec<mpsc::Sender<Bytes>>>>>;

enum Command {
    Subscribe(u64),
    Unsubscribe(u64),
}

pub struct Push {
    registry: Registry,
    commands: mpsc::UnboundedSender<Command>,
    valkey: redis::aio::MultiplexedConnection,
    node: String,
}

impl Push {
    pub async fn connect(addr: &str, node: String) -> Result<Self, redis::RedisError> {
        let client = redis::Client::open(format!("redis://{addr}/"))?;
        let valkey = client.get_multiplexed_async_connection().await?;
        let pubsub = client.get_async_pubsub().await?;
        let registry: Registry = Arc::new(Mutex::new(HashMap::new()));
        let (commands, receiver) = mpsc::unbounded_channel();
        tokio::spawn(run(pubsub, receiver, registry.clone()));
        Ok(Self {
            registry,
            commands,
            valkey,
            node,
        })
    }

    pub async fn attach(&self, user_id: u64, device_id: u64, out: mpsc::Sender<Bytes>) {
        let first = {
            let mut reg = self.registry.lock().await;
            let conns = reg.entry(user_id).or_default();
            conns.push(out);
            conns.len() == 1
        };
        if first {
            let _ = self.commands.send(Command::Subscribe(user_id));
        }
        let mut valkey = self.valkey.clone();
        let _: Result<(), _> = valkey
            .hset(
                format!("presence:{user_id}"),
                device_id.to_string(),
                &self.node,
            )
            .await;
    }

    pub async fn detach(&self, user_id: u64, device_id: u64, out: &mpsc::Sender<Bytes>) {
        let last = {
            let mut reg = self.registry.lock().await;
            if let Some(conns) = reg.get_mut(&user_id) {
                conns.retain(|c| !c.same_channel(out));
                if conns.is_empty() {
                    reg.remove(&user_id);
                    true
                } else {
                    false
                }
            } else {
                false
            }
        };
        if last {
            let _ = self.commands.send(Command::Unsubscribe(user_id));
        }
        let mut valkey = self.valkey.clone();
        let _: Result<(), _> = valkey
            .hdel(format!("presence:{user_id}"), device_id.to_string())
            .await;
    }
}

async fn run(
    pubsub: redis::aio::PubSub,
    mut commands: mpsc::UnboundedReceiver<Command>,
    registry: Registry,
) {
    let (mut sink, mut stream) = pubsub.split();
    loop {
        tokio::select! {
            command = commands.recv() => match command {
                Some(Command::Subscribe(user)) => {
                    if let Err(e) = sink.subscribe(format!("push:{user}")).await {
                        tracing::warn!(error = %e, "subscribe failed");
                    }
                }
                Some(Command::Unsubscribe(user)) => {
                    let _ = sink.unsubscribe(format!("push:{user}")).await;
                }
                None => return,
            },
            message = stream.next() => {
                let Some(message) = message else { continue };
                let channel = message.get_channel_name();
                let Ok(user) = channel.trim_start_matches("push:").parse::<u64>() else { continue };
                let frame = event_frame(message.get_payload_bytes());
                let targets: Vec<mpsc::Sender<Bytes>> = registry.lock().await.get(&user).cloned().unwrap_or_default();
                for out in targets {
                    // A full queue closes the device's connection elsewhere; here the push is dropped.
                    let _ = out.try_send(frame.clone());
                }
            }
        }
    }
}
