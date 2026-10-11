//! The metrics every backend exposes (docs/metrics.md), in the Prometheus
//! text format. There is no collector, so no live memory figure; resident
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
    buckets: [u64; BUCKETS.len()],
    count: u64,
    sum: f64,
}

impl Histogram {
    fn observe(&mut self, seconds: f64) {
        self.count += 1;
        self.sum += seconds;
        for (bucket, le) in self.buckets.iter_mut().zip(BUCKETS) {
            if seconds <= le {
                *bucket += 1;
            }
        }
    }
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
        self.durations
            .lock()
            .unwrap()
            .entry(kind.to_string())
            .or_default()
            .observe(duration.as_secs_f64());
    }

    pub fn render(&self) -> String {
        // Writing to a String cannot fail.
        let mut out = String::new();
        let _ = self.write(&mut out);
        out
    }

    fn write(&self, out: &mut String) -> std::fmt::Result {
        writeln!(out, "# TYPE wrinkle_connections gauge")?;
        writeln!(
            out,
            "wrinkle_connections {}",
            self.connections.load(Ordering::Relaxed)
        )?;
        writeln!(out, "# TYPE wrinkle_events_pushed_total counter")?;
        writeln!(
            out,
            "wrinkle_events_pushed_total {}",
            self.pushed.load(Ordering::Relaxed)
        )?;

        writeln!(out, "# TYPE wrinkle_requests_total counter")?;
        for ((kind, code), n) in self.requests.lock().unwrap().iter() {
            writeln!(
                out,
                "wrinkle_requests_total{{type=\"{kind}\",code=\"{code}\"}} {n}"
            )?;
        }

        writeln!(out, "# TYPE wrinkle_request_duration_seconds histogram")?;
        for (kind, h) in self.durations.lock().unwrap().iter() {
            let name = "wrinkle_request_duration_seconds";
            for (n, le) in h.buckets.iter().zip(BUCKETS) {
                writeln!(out, "{name}_bucket{{type=\"{kind}\",le=\"{le}\"}} {n}")?;
            }
            writeln!(
                out,
                "{name}_bucket{{type=\"{kind}\",le=\"+Inf\"}} {}",
                h.count
            )?;
            writeln!(out, "{name}_count{{type=\"{kind}\"}} {}", h.count)?;
            writeln!(out, "{name}_sum{{type=\"{kind}\"}} {}", h.sum)?;
        }

        writeln!(out, "# TYPE process_cpu_seconds_total counter")?;
        writeln!(out, "process_cpu_seconds_total {}", cpu_seconds())?;
        if let Some(usage) = memory_stats::memory_stats() {
            writeln!(out, "# TYPE process_resident_memory_bytes gauge")?;
            writeln!(out, "process_resident_memory_bytes {}", usage.physical_mem)?;
        }
        Ok(())
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
    let seconds = |t: libc::timeval| t.tv_sec as f64 + t.tv_usec as f64 / 1e6;
    seconds(usage.ru_utime) + seconds(usage.ru_stime)
}
