//! Which gateway instance holds each device's connection: one Valkey hash per
//! user, device to instance id. Each instance keeps `gateway:<instance>`
//! alive while it runs; the services count a device as connected only while
//! its instance's key exists.

use std::hash::{BuildHasher, Hasher, RandomState};
use std::time::Duration;

use redis::aio::MultiplexedConnection;
use redis::{AsyncCommands, Script};

const INSTANCE_TTL: Duration = Duration::from_secs(90);
const HEARTBEAT: Duration = Duration::from_secs(30);

/// Removes the device's field only while it still names this instance, so a
/// device that reconnected elsewhere keeps its new entry.
const REMOVE_IF_OURS: &str = "if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then \
     return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0";

pub struct Presence {
    valkey: MultiplexedConnection,
    instance: String,
    remove_if_ours: Script,
}

impl Presence {
    pub async fn connect(client: &redis::Client, node: &str) -> redis::RedisResult<Self> {
        let valkey = client.get_multiplexed_async_connection().await?;
        let presence = Self {
            valkey,
            instance: instance_id(node),
            remove_if_ours: Script::new(REMOVE_IF_OURS),
        };
        presence.beat().await?;
        Ok(presence)
    }

    pub fn instance(&self) -> &str {
        &self.instance
    }

    pub async fn set(&self, user_id: u64, device_id: u64) {
        let mut valkey = self.valkey.clone();
        let _: redis::RedisResult<()> = valkey
            .hset(key(user_id), device_id.to_string(), &self.instance)
            .await;
    }

    pub async fn remove(&self, user_id: u64, device_id: u64) {
        let mut valkey = self.valkey.clone();
        let _: redis::RedisResult<i64> = self
            .remove_if_ours
            .key(key(user_id))
            .arg(device_id.to_string())
            .arg(&self.instance)
            .invoke_async(&mut valkey)
            .await;
    }

    /// Refreshes the instance key until the task is aborted.
    pub async fn heartbeat(&self) {
        let mut tick = tokio::time::interval(HEARTBEAT);
        tick.tick().await;
        loop {
            tick.tick().await;
            if let Err(e) = self.beat().await {
                tracing::warn!(error = %e, "presence heartbeat failed");
            }
        }
    }

    /// Drops the instance key, so its remaining entries count as stale.
    pub async fn retire(&self) {
        let mut valkey = self.valkey.clone();
        let _: redis::RedisResult<()> = valkey.del(instance_key(&self.instance)).await;
    }

    async fn beat(&self) -> redis::RedisResult<()> {
        let mut valkey = self.valkey.clone();
        valkey
            .set_ex(instance_key(&self.instance), 1, INSTANCE_TTL.as_secs())
            .await
    }
}

/// `<node>/<16 random hex chars>`; the standard library's hasher keys are
/// random per process, which is all an instance id needs.
fn instance_id(node: &str) -> String {
    let random = RandomState::new().build_hasher().finish();
    format!("{node}/{random:016x}")
}

fn key(user_id: u64) -> String {
    format!("presence:{user_id}")
}

fn instance_key(instance: &str) -> String {
    format!("gateway:{instance}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn instance_ids_are_node_and_random_hex() {
        let a = instance_id("node-1");
        let b = instance_id("node-1");
        let (node, random) = a.split_once('/').unwrap();
        assert_eq!(node, "node-1");
        assert_eq!(random.len(), 16);
        assert!(random.chars().all(|c| c.is_ascii_hexdigit()));
        assert_ne!(a, b);
    }
}
