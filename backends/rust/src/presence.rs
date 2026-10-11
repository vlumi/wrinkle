//! Which node holds each device's connection: one Valkey hash per user,
//! device to node. The services read it to notify the devices that are not
//! connected.

use redis::AsyncCommands;
use redis::aio::MultiplexedConnection;

pub struct Presence {
    valkey: MultiplexedConnection,
    node: String,
}

impl Presence {
    pub async fn connect(client: &redis::Client, node: String) -> redis::RedisResult<Self> {
        let valkey = client.get_multiplexed_async_connection().await?;
        Ok(Self { valkey, node })
    }

    pub async fn set(&self, user_id: u64, device_id: u64) {
        let mut valkey = self.valkey.clone();
        let _: redis::RedisResult<()> = valkey
            .hset(key(user_id), device_id.to_string(), &self.node)
            .await;
    }

    pub async fn remove(&self, user_id: u64, device_id: u64) {
        let mut valkey = self.valkey.clone();
        let _: redis::RedisResult<()> = valkey.hdel(key(user_id), device_id.to_string()).await;
    }
}

fn key(user_id: u64) -> String {
    format!("presence:{user_id}")
}
