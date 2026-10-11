//! The Rust gateway: one tokio task per connection, requests forwarded to a
//! services process over the internal RPC, pushes through Valkey pub/sub.
//! A gateway only; run.sh starts the Go services and fan-out beside it.

mod conn;
mod metrics;
mod presence;
mod push;

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::{Duration, Instant};

use axum::Router;
use axum::extract::{ConnectInfo, Request, State};
use axum::http::StatusCode;
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::Notify;
use tonic::transport::Channel;

/// How long in-flight requests and closing connections get on shutdown.
const DRAIN_TIMEOUT: Duration = Duration::from_secs(5);
/// How long the HTTP server then gets to stop.
const SERVER_STOP_TIMEOUT: Duration = Duration::from_secs(3);

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
    pub presence: presence::Presence,
    pub metrics: Arc<metrics::Metrics>,
    pub conns: conn::Conns,
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
    let valkey = redis::Client::open(format!("redis://{valkey}/"))?;
    let metrics = Arc::new(metrics::Metrics::default());
    let presence = presence::Presence::connect(&valkey, &node).await?;
    let push = push::Push::connect(&valkey, metrics.clone()).await?;
    let app = Arc::new(App {
        backend,
        push,
        presence,
        metrics,
        conns: conn::Conns::default(),
    });
    let keepalive = tokio::spawn({
        let app = app.clone();
        async move { app.conns.keepalive().await }
    });
    let heartbeat = tokio::spawn({
        let app = app.clone();
        async move { app.presence.heartbeat().await }
    });

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    tracing::info!(%addr, services, instance = app.presence.instance(), "gateway listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    let stop = Arc::new(Notify::new());
    let mut server = tokio::spawn({
        let stop = stop.clone();
        let service = router(app.clone()).into_make_service_with_connect_info::<SocketAddr>();
        async move {
            axum::serve(listener, service)
                .with_graceful_shutdown(async move { stop.notified().await })
                .await
        }
    });

    tokio::select! {
        () = shutdown_signal() => {}
        result = &mut server => {
            result??;
            return Ok(());
        }
    }
    tracing::info!("draining");
    keepalive.abort();
    heartbeat.abort();
    drain(&app).await;
    stop.notify_one();
    if tokio::time::timeout(SERVER_STOP_TIMEOUT, server)
        .await
        .is_err()
    {
        tracing::warn!("server did not stop in time");
    }
    tracing::info!("stopped");
    Ok(())
}

async fn shutdown_signal() {
    let mut terminate = signal(SignalKind::terminate()).expect("SIGTERM handler");
    tokio::select! {
        _ = tokio::signal::ctrl_c() => {}
        _ = terminate.recv() => {}
    }
}

/// Closes every connection, which removes its presence entry, and gives
/// in-flight requests a moment to finish.
async fn drain(app: &App) {
    app.conns.drain();
    let deadline = Instant::now() + DRAIN_TIMEOUT;
    while !app.conns.settled() && Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    app.presence.retire().await;
}

fn router(app: Arc<App>) -> Router {
    Router::new()
        .route("/v1/ws", get(conn::upgrade))
        .route("/healthz", get(healthz))
        .route(
            "/metrics",
            get(|State(app): State<Arc<App>>| async move { app.metrics.render() }),
        )
        .nest(
            "/debug",
            Router::new()
                // The load generator collects garbage before reading live
                // memory; there is no collector here, so it only answers.
                .route("/gc", post(|| async { "ok\n" }))
                .layer(middleware::from_fn(loopback_only)),
        )
        .with_state(app)
}

async fn healthz(State(app): State<Arc<App>>) -> Response {
    if app.conns.draining() {
        (StatusCode::SERVICE_UNAVAILABLE, "draining\n").into_response()
    } else {
        "ok\n".into_response()
    }
}

async fn loopback_only(
    ConnectInfo(peer): ConnectInfo<SocketAddr>,
    request: Request,
    next: Next,
) -> Response {
    if peer.ip().to_canonical().is_loopback() {
        next.run(request).await
    } else {
        StatusCode::FORBIDDEN.into_response()
    }
}

fn env(key: &str, fallback: &str) -> String {
    std::env::var(key)
        .ok()
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| fallback.to_string())
}

fn hostname() -> String {
    std::process::Command::new("hostname")
        .output()
        .ok()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "localhost".to_string())
}
