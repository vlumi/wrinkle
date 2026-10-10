//! The Rust gateway: one tokio task per connection, requests forwarded to a
//! services process over the internal RPC, pushes through Valkey pub/sub.
//! A gateway only; run.sh starts the Go services and fan-out beside it.

mod conn;
mod metrics;
mod push;

use std::net::SocketAddr;
use std::sync::Arc;

use axum::Router;
use axum::extract::State;
use axum::routing::{get, post};
use tonic::transport::Channel;

// Nested as the packages are, so the generated cross-references resolve.
pub mod pb {
    pub mod v1 {
        tonic::include_proto!("wrinkle.v1");
    }
    pub mod backend {
        pub mod v1 {
            tonic::include_proto!("wrinkle.backend.v1");
        }
    }
}

pub type BackendClient = pb::backend::v1::backend_service_client::BackendServiceClient<Channel>;

pub struct App {
    pub backend: BackendClient,
    pub push: push::Push,
    pub metrics: metrics::Metrics,
}

fn env(key: &str, fallback: &str) -> String {
    std::env::var(key)
        .ok()
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| fallback.to_string())
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::from_default_env().add_directive("info".parse()?),
        )
        .init();

    let listen = env("WRINKLE_LISTEN", ":8080");
    let port: u16 = listen.rsplit(':').next().unwrap_or("8080").parse()?;
    let services = env("WRINKLE_SERVICES", "localhost:9000");
    let valkey = env("WRINKLE_VALKEY", "localhost:6379");
    let node = env("WRINKLE_NODE", &hostname());

    let backend = BackendClient::connect(format!("http://{services}")).await?;
    let push = push::Push::connect(&valkey, node).await?;
    let app = Arc::new(App {
        backend,
        push,
        metrics: metrics::Metrics::default(),
    });

    let router = Router::new()
        .route("/v1/ws", get(conn::upgrade))
        .route("/healthz", get(|| async { "ok\n" }))
        .route(
            "/metrics",
            get(|State(app): State<Arc<App>>| async move { app.metrics.render() }),
        )
        .route("/debug/gc", post(|| async { "ok\n" }))
        .with_state(app);

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    tracing::info!(%addr, services, "gateway listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, router)
        .with_graceful_shutdown(async {
            let _ = tokio::signal::ctrl_c().await;
        })
        .await?;
    Ok(())
}

fn hostname() -> String {
    std::process::Command::new("hostname")
        .output()
        .ok()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "localhost".to_string())
}
