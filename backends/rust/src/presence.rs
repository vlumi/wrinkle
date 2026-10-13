//! Which gateway instance holds each device's connection: one Valkey hash per
//! user, device to instance id. Each instance keeps `gateway:<instance>`
//! alive while it runs; the services count a device as connected only while
//! its instance's key exists.

use std::hash::{BuildHasher, Hasher, RandomState};
use std::time::Duration;

use redis::aio::{ConnectionManager, ConnectionManagerConfig};
use redis::{AsyncCommands, Script};

use crate::user_id::{self, UserId};

const INSTANCE_TTL: Duration = Duration::from_secs(90);
const HEARTBEAT: Duration = Duration::from_secs(30);
/// How long one command, or one connection attempt, may take.
const COMMAND_TIMEOUT: Duration = Duration::from_secs(5);

/// Removes the device's field only while it still names this instance, so a
/// device that reconnected elsewhere keeps its new entry.
const REMOVE_IF_OURS: &str = "if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then \
     return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0";

pub struct Presence {
    valkey: ConnectionManager,
    instance: String,
    remove_if_ours: Script,
}

impl Presence {
    pub async fn connect(client: &redis::Client, node: &str) -> redis::RedisResult<Self> {
        let config = ConnectionManagerConfig::new()
            .set_response_timeout(Some(COMMAND_TIMEOUT))
            .set_connection_timeout(Some(COMMAND_TIMEOUT));
        let valkey = ConnectionManager::new_with_config(client.clone(), config).await?;
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

    pub async fn set(&self, user_id: UserId, device_id: u64) {
        let result: redis::RedisResult<()> = self
            .retrying(|mut valkey| {
                let instance = self.instance.clone();
                async move {
                    valkey
                        .hset(key(user_id), device_id.to_string(), instance)
                        .await
                }
            })
            .await;
        if let Err(e) = result {
            tracing::warn!(error = %e, user = user_id::hex(&user_id), device_id, "presence set failed");
        }
    }

    pub async fn remove(&self, user_id: UserId, device_id: u64) {
        let result: redis::RedisResult<i64> = self
            .retrying(|mut valkey| {
                let mut invocation = self.remove_if_ours.key(key(user_id));
                invocation.arg(device_id.to_string()).arg(&self.instance);
                async move { invocation.invoke_async(&mut valkey).await }
            })
            .await;
        if let Err(e) = result {
            tracing::warn!(error = %e, user = user_id::hex(&user_id), device_id, "presence remove failed");
        }
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
        self.retrying(|mut valkey| {
            let key = instance_key(&self.instance);
            async move { valkey.set_ex(key, 1, INSTANCE_TTL.as_secs()).await }
        })
        .await
    }

    /// Runs `op` once more if the connection had dropped; the manager
    /// reconnects after the first failure. A timeout is not retried, so a
    /// stalled Valkey costs a write at most `COMMAND_TIMEOUT`.
    async fn retrying<T, F, Fut>(&self, op: F) -> redis::RedisResult<T>
    where
        F: Fn(ConnectionManager) -> Fut,
        Fut: Future<Output = redis::RedisResult<T>>,
    {
        match op(self.valkey.clone()).await {
            Err(e) if !e.is_timeout() && (e.is_connection_dropped() || e.is_io_error()) => {
                op(self.valkey.clone()).await
            }
            result => result,
        }
    }
}

/// `<node>/<16 random hex chars>`; the standard library's hasher keys are
/// random per process, which is all an instance id needs.
fn instance_id(node: &str) -> String {
    let random = RandomState::new().build_hasher().finish();
    format!("{node}/{random:016x}")
}

fn key(user_id: UserId) -> String {
    format!("presence:{}", user_id::hex(&user_id))
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
