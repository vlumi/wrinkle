//! The metrics every backend exposes (docs/metrics.md), in the Prometheus
//! text format. Rust has no collector, so there is no live figure; resident
//! memory comes from the OS.

use std::collections::BTreeMap;
use std::fmt::Write;
use std::sync::Mutex;
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::time::Duration;

const BUCKETS: [f64; 16] = [
    0.0005, 0.001, 0.002, 0.004, 0.008, 0.016, 0.032, 0.064, 0.128, 0.256, 0.512, 1.024, 2.048,
    4.096, 8.192, 16.384,
];

#[derive(Default)]
struct Histogram {
    buckets: [u64; 16],
    count: u64,
    sum: f64,
}

#[derive(Default)]
pub struct Metrics {
    connections: AtomicI64,
    pushed: AtomicU64,
    requests: Mutex<BTreeMap<(String, String), u64>>,
    durations: Mutex<BTreeMap<String, Histogram>>,
}

impl Metrics {
    pub fn connection(&self, delta: i64) {
        self.connections.fetch_add(delta, Ordering::Relaxed);
    }

    pub fn pushed(&self) {
        self.pushed.fetch_add(1, Ordering::Relaxed);
    }

    pub fn request(&self, kind: &str, code: &str, duration: Duration) {
        *self
            .requests
            .lock()
            .unwrap()
            .entry((kind.to_string(), code.to_string()))
            .or_default() += 1;
        let seconds = duration.as_secs_f64();
        let mut durations = self.durations.lock().unwrap();
        let h = durations.entry(kind.to_string()).or_default();
        h.count += 1;
        h.sum += seconds;
        for (i, le) in BUCKETS.iter().enumerate() {
            if seconds <= *le {
                h.buckets[i] += 1;
            }
        }
    }

    pub fn render(&self) -> String {
        let mut out = String::new();
        let _ = writeln!(
            out,
            "# TYPE wrinkle_connections gauge\nwrinkle_connections {}",
            self.connections.load(Ordering::Relaxed)
        );
        let _ = writeln!(
            out,
            "# TYPE wrinkle_events_pushed_total counter\nwrinkle_events_pushed_total {}",
            self.pushed.load(Ordering::Relaxed)
        );
        let _ = writeln!(out, "# TYPE wrinkle_requests_total counter");
        for ((kind, code), n) in self.requests.lock().unwrap().iter() {
            let _ = writeln!(
                out,
                "wrinkle_requests_total{{type=\"{kind}\",code=\"{code}\"}} {n}"
            );
        }
        let _ = writeln!(out, "# TYPE wrinkle_request_duration_seconds histogram");
        for (kind, h) in self.durations.lock().unwrap().iter() {
            for (i, le) in BUCKETS.iter().enumerate() {
                let _ = writeln!(
                    out,
                    "wrinkle_request_duration_seconds_bucket{{type=\"{kind}\",le=\"{le}\"}} {}",
                    h.buckets[i]
                );
            }
            let _ = writeln!(
                out,
                "wrinkle_request_duration_seconds_bucket{{type=\"{kind}\",le=\"+Inf\"}} {}",
                h.count
            );
            let _ = writeln!(
                out,
                "wrinkle_request_duration_seconds_count{{type=\"{kind}\"}} {}",
                h.count
            );
            let _ = writeln!(
                out,
                "wrinkle_request_duration_seconds_sum{{type=\"{kind}\"}} {}",
                h.sum
            );
        }
        let _ = writeln!(
            out,
            "# TYPE process_cpu_seconds_total counter\nprocess_cpu_seconds_total {}",
            cpu_seconds()
        );
        if let Some(usage) = memory_stats::memory_stats() {
            let _ = writeln!(
                out,
                "# TYPE process_resident_memory_bytes gauge\nprocess_resident_memory_bytes {}",
                usage.physical_mem
            );
        }
        out
    }
}

fn cpu_seconds() -> f64 {
    let mut usage = std::mem::MaybeUninit::<libc::rusage>::uninit();
    // SAFETY: getrusage fills the struct for the calling process.
    let usage = unsafe {
        if libc::getrusage(libc::RUSAGE_SELF, usage.as_mut_ptr()) != 0 {
            return 0.0;
        }
        usage.assume_init()
    };
    let tv = |t: libc::timeval| t.tv_sec as f64 + t.tv_usec as f64 / 1e6;
    tv(usage.ru_utime) + tv(usage.ru_stime)
}
