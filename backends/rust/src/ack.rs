//! A connection's acks: the highest cursor is forwarded at most once per
//! interval, and at once when the connection ends. One task per connection
//! that has acked writes them, one at a time, sleeping between writes.

use std::future::Future;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use tokio::sync::Notify;
use tokio::time::Instant;

/// Forwards one cursor; failures are its own to log.
pub trait Write: Send + Sync + 'static {
    fn write(&self, cursor: u64) -> impl Future<Output = ()> + Send;
}

pub struct Acks<W> {
    interval: Duration,
    writer: W,
    /// Live writer tasks across the gateway, which the drain waits for.
    tasks: Arc<AtomicUsize>,
    state: Mutex<State>,
    closed: Notify,
}

#[derive(Default)]
struct State {
    /// The highest cursor not yet written.
    pending: Option<u64>,
    last_start: Option<Instant>,
    /// A writer task exists: writing, or waiting to.
    running: bool,
    closed: bool,
}

impl<W: Write> Acks<W> {
    pub fn new(interval: Duration, writer: W, tasks: Arc<AtomicUsize>) -> Arc<Self> {
        Arc::new(Self {
            interval,
            writer,
            tasks,
            state: Mutex::default(),
            closed: Notify::new(),
        })
    }

    pub fn writer(&self) -> &W {
        &self.writer
    }

    pub fn ack(self: &Arc<Self>, cursor: u64) {
        let mut state = self.state.lock().unwrap();
        if state.closed {
            return;
        }
        state.pending = state.pending.max(Some(cursor));
        if state.running {
            return;
        }
        state.running = true;
        drop(state);
        self.tasks.fetch_add(1, Ordering::SeqCst);
        tokio::spawn(self.clone().run());
    }

    /// Writes the pending cursor at once, after a write in flight; the task
    /// is spawned, so it outlives the connection.
    pub fn close(&self) {
        self.state.lock().unwrap().closed = true;
        self.closed.notify_one();
    }

    async fn run(self: Arc<Self>) {
        loop {
            let next = {
                let mut state = self.state.lock().unwrap();
                let Some(cursor) = state.pending else {
                    state.running = false;
                    break;
                };
                let now = Instant::now();
                match state.last_start.map(|start| start + self.interval) {
                    Some(due) if due > now && !state.closed => Err(due),
                    _ => {
                        state.pending = None;
                        state.last_start = Some(now);
                        Ok(cursor)
                    }
                }
            };
            match next {
                Ok(cursor) => self.writer.write(cursor).await,
                Err(due) => tokio::select! {
                    () = tokio::time::sleep_until(due) => {}
                    () = self.closed.notified() => {}
                },
            }
        }
        self.tasks.fetch_sub(1, Ordering::SeqCst);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const INTERVAL: Duration = Duration::from_secs(60);
    const WRITE_TIME: Duration = Duration::from_secs(1);

    /// Records each write's start and cursor, taking `WRITE_TIME` each.
    #[derive(Clone, Default)]
    struct Recorder {
        writes: Arc<Mutex<Vec<(Duration, u64)>>>,
        writing: Arc<AtomicUsize>,
        overlapped: Arc<AtomicUsize>,
        origin: Arc<Mutex<Option<Instant>>>,
    }

    impl Write for Recorder {
        fn write(&self, cursor: u64) -> impl Future<Output = ()> + Send {
            let this = self.clone();
            async move {
                let origin = this.origin.lock().unwrap().expect("origin");
                if this.writing.fetch_add(1, Ordering::SeqCst) > 0 {
                    this.overlapped.fetch_add(1, Ordering::SeqCst);
                }
                this.writes
                    .lock()
                    .unwrap()
                    .push((Instant::now() - origin, cursor));
                tokio::time::sleep(WRITE_TIME).await;
                this.writing.fetch_sub(1, Ordering::SeqCst);
            }
        }
    }

    fn setup(interval: Duration) -> (Arc<Acks<Recorder>>, Recorder, Arc<AtomicUsize>) {
        let recorder = Recorder::default();
        *recorder.origin.lock().unwrap() = Some(Instant::now());
        let tasks = Arc::new(AtomicUsize::default());
        let acks = Acks::new(interval, recorder.clone(), tasks.clone());
        (acks, recorder, tasks)
    }

    fn writes(recorder: &Recorder) -> Vec<(u64, u64)> {
        let writes = recorder.writes.lock().unwrap();
        writes.iter().map(|(at, c)| (at.as_secs(), *c)).collect()
    }

    async fn advance(d: Duration) {
        tokio::time::sleep(d).await;
    }

    #[tokio::test(start_paused = true)]
    async fn the_first_ack_is_written_at_once() {
        let (acks, recorder, tasks) = setup(INTERVAL);
        acks.ack(5);
        advance(Duration::from_millis(1)).await;
        assert_eq!(writes(&recorder), [(0, 5)]);
        advance(WRITE_TIME).await;
        assert_eq!(tasks.load(Ordering::SeqCst), 0);
    }

    #[tokio::test(start_paused = true)]
    async fn acks_within_the_interval_are_coalesced_and_written_once() {
        let (acks, recorder, tasks) = setup(INTERVAL);
        acks.ack(5);
        advance(Duration::from_secs(10)).await;
        acks.ack(9);
        advance(Duration::from_secs(10)).await;
        acks.ack(7);
        advance(Duration::from_secs(30)).await;
        assert_eq!(writes(&recorder), [(0, 5)]);
        advance(Duration::from_secs(15)).await;
        assert_eq!(writes(&recorder), [(0, 5), (60, 9)]);
        advance(INTERVAL * 2).await;
        assert_eq!(writes(&recorder), [(0, 5), (60, 9)]);
        assert_eq!(tasks.load(Ordering::SeqCst), 0);
        // Long idle: the next ack is written at once.
        acks.ack(11);
        advance(Duration::from_millis(1)).await;
        assert_eq!(writes(&recorder).last(), Some(&(185, 11)));
    }

    #[tokio::test(start_paused = true)]
    async fn an_ack_during_a_write_is_written_an_interval_after_its_start() {
        let (acks, recorder, _) = setup(INTERVAL);
        acks.ack(5);
        advance(WRITE_TIME / 2).await;
        acks.ack(6);
        advance(Duration::from_secs(70)).await;
        assert_eq!(writes(&recorder), [(0, 5), (60, 6)]);
        assert_eq!(recorder.overlapped.load(Ordering::SeqCst), 0);
    }

    #[tokio::test(start_paused = true)]
    async fn closing_writes_the_pending_cursor_at_once() {
        let (acks, recorder, tasks) = setup(INTERVAL);
        acks.ack(5);
        advance(Duration::from_secs(10)).await;
        acks.ack(9);
        acks.close();
        advance(Duration::from_millis(1)).await;
        assert_eq!(writes(&recorder), [(0, 5), (10, 9)]);
        advance(WRITE_TIME).await;
        assert_eq!(tasks.load(Ordering::SeqCst), 0);
        // The scheduled write is gone, and later acks are ignored.
        acks.ack(12);
        advance(INTERVAL * 2).await;
        assert_eq!(writes(&recorder), [(0, 5), (10, 9)]);
    }

    #[tokio::test(start_paused = true)]
    async fn closing_during_a_write_waits_for_it() {
        let (acks, recorder, tasks) = setup(INTERVAL);
        acks.ack(5);
        advance(WRITE_TIME / 2).await;
        acks.ack(9);
        acks.close();
        advance(WRITE_TIME).await;
        assert_eq!(recorder.overlapped.load(Ordering::SeqCst), 0);
        let writes = recorder.writes.lock().unwrap().clone();
        assert_eq!(writes, [(Duration::ZERO, 5), (WRITE_TIME, 9)]);
        advance(WRITE_TIME).await;
        assert_eq!(tasks.load(Ordering::SeqCst), 0);
    }

    #[tokio::test(start_paused = true)]
    async fn closing_with_nothing_pending_writes_nothing() {
        let (acks, recorder, tasks) = setup(INTERVAL);
        acks.close();
        advance(INTERVAL).await;
        assert!(writes(&recorder).is_empty());
        assert_eq!(tasks.load(Ordering::SeqCst), 0);
    }

    #[tokio::test(start_paused = true)]
    async fn interval_zero_writes_each_ack_one_at_a_time() {
        let (acks, recorder, _) = setup(Duration::ZERO);
        acks.ack(5);
        advance(Duration::from_millis(1)).await;
        acks.ack(9);
        acks.ack(7);
        advance(WRITE_TIME).await;
        acks.ack(10);
        advance(WRITE_TIME * 3).await;
        assert_eq!(writes(&recorder), [(0, 5), (1, 9), (2, 10)]);
        assert_eq!(recorder.overlapped.load(Ordering::SeqCst), 0);
    }
}
