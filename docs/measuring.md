# Measuring

How backends are compared. Absolute numbers depend on the machine and say little about production; what matters is that every backend is measured the same way on the same machine and infrastructure, in the same session.

## Setup

- The shared infrastructure runs in Docker Desktop (`make infra-up`) with the memory caps in `infra/docker-compose.yml`. Record Docker's CPU and memory allocation.
- The backend under test runs natively on the host as one process with all roles, listening on port 8080. It is restarted fresh before every run, so memory figures start from the same baseline.
- The load generator runs natively on the host too. It shares the CPU with the backend; keep the load far from saturating the machine, and compare backends against each other rather than against the hardware.
- Each backend's protocol run starts from a freshly created stack (`FRESH=1`, which `make measure-*` sets): end-to-end latency at 500 messages per second went from 66 ms on an empty database to 1.4 s after a day of runs, with a million rows in the user logs, so runs on a grown stack are not comparable with runs on a fresh one.
- Nothing else of note runs on the machine.

## Runs

`loadgen/protocol.sh <backend command>` performs these, appending one JSON line per run to the results file:

1. **Idle connections.** 500, 1000, 2000 and 4000 devices, one per user, no messages. Warm-up 10 s, then 30 s. Memory per connection is the slope over the connection count, from a least-squares fit across the four points, which cancels the process baseline; report the baseline too. The load generator samples both resident memory and the runtime's live memory after asking the backend to collect garbage (`POST /debug/gc`); quote the live slope, with the RSS slope beside it.
2. **Latency at fixed rates.** 1000 devices, one per user, at 100, 500, 1000 and 2000 messages per second. Warm-up 15 s, then 60 s. Report end-to-end p50 and p99 (send on one device to receipt on the peer's device), the send round trip p50, CPU time per thousand messages, and the share of messages received within the 10 s grace after the window.
3. **Highest sustained rate.** The highest step of run 2 whose end-to-end p99 stays under 1 s with no losses, refined with extra runs between the steps when the write-up needs it.

Each series is run `REPS` times (default 1); report the median run.

## Recording

Results live in `docs/results/<date>-<backend>.md` with the raw JSON lines beside them. Each file records:

- date, commit, backend and its version of the runtime (Go, JDK, ...);
- machine: model, chip, memory, OS version, Docker Desktop CPU and memory allocation;
- configuration: anything that differs from the defaults in `infra/` and the backend's flags.

Never compare numbers taken on different machines or infrastructure settings; take a fresh baseline instead.

## Caveats

- End-to-end latency includes the change-data path: Debezium reading the binlog through vtgate, Kafka, and the fan-out worker. That part is shared by every backend and dominates at low load. Its own figure is the backend's `wrinkle_fanout_lag_seconds` histogram.
- The database and brokers are capped in memory and run under Docker Desktop's virtual machine; they may saturate before a backend does. Watch their CPU in `docker stats` during the high-rate runs and note it.
