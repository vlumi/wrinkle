# Measuring

How backends are compared. Absolute numbers depend on the machine and say little about production; what matters is that every backend is measured the same way on the same machine and infrastructure, in the same session.

## Setup

- The shared infrastructure runs in Docker Desktop (`make infra-up`) with the memory caps in `infra/docker-compose.yml`. Record Docker's CPU and memory allocation.
- The backend under test runs natively on the host, listening on port 8080, without the balancers: the monoliths (Go, Java, TypeScript) as one process with every role; the gateways (Go, Java, TypeScript, Elixir, Rust) with one services process beside them, Java's own for the Java gateway, TypeScript's for the TypeScript gateway and Go's for the others, where only the gateway is measured. It is restarted fresh before every run, so memory figures start from the same baseline. The side-by-side setup and the cluster are for trying the scaled shape, not for these measurements.
- macOS starts shells with a limit of 256 open files. Go and the JVM raise their own limit; for the Elixir and Rust gateways, the launcher they share (`backends/beside-services.sh`) raises it, or they would refuse connections past about 200.
- The load generator runs natively on the host too. It shares the CPU with the backend; keep the load far from saturating the machine, and compare backends against each other rather than against the hardware.
- Each backend's protocol run starts from a freshly created stack (`FRESH=1`, which `make measure-*` sets): end-to-end latency at 500 messages per second went from 66 ms on an empty database to 1.4 s after a day of runs, with a million rows in the user logs, so runs on a grown stack are not comparable with runs on a fresh one.
- Nothing else of note runs on the machine.

## Runs

`make measure-<backend>` (`go-mono`, `java-mono`, `ts-mono`, `go-gw`, `java-gw`, `ts-gw`, `elixir-gw`, `rust-gw`, or `all`) runs `loadgen/protocol.sh`, which performs these, appending one JSON line per run to `docs/results/<date>-<backend>.jsonl`. For a single ad-hoc run against whatever backend is running, `make load CONNECTIONS=… RATE=… DURATION=…`, with any other flag in `LOADGEN_ARGS` (for example `LOADGEN_ARGS="--group-share 0.5"`).

The load generator's users are `lg-0`, `lg-1`, ... with a fixed password. Before connecting, it registers them, or signs in those that exist, and connects with the device tokens; that phase is outside every window. Each sign-in hashes a password (about 19 MiB, tens of milliseconds), which inflates the services' resident memory after the phase; the live-memory figures, taken after a forced collection, are unaffected. Results from before accounts (2026-10-11 and earlier) connected by name without that phase.

Users are paired into direct conversations, `lg-2n` with `lg-2n+1`. `--group-share` (0 to 1, default 0) sends that share of each device's messages, chosen at random per send, to a group instead; `--group-size` (default 8, up to 256) sets its size. Before the warm-up, consecutive users are put in groups of that size, each created by its first user with a client group id naming the size and the block, so later runs find the same groups; users left over send only directly. Groups take only the creator's contacts, so first the creator and each member ask each other and the setup waits, up to 30 s, until the creator's list shows every member as a contact; asking again changes nothing, so later runs pass through. A group message is expected on every device of the other members, so the expected receipts count those; the results record `group_size`, `group_share` and `group_sent`. With the default share of 0 no groups are formed and runs are as before. The protocol below uses no groups.

Content is end-to-end encrypted as the clients do it, with the Go client's key manager (`clients/go`). Each device generates an X25519 key pair when it connects and publishes the public key; the devices earlier runs left signed in are then signed out, so keys are wrapped only for this run's devices. Before the warm-up every conversation gets a key epoch: one of its devices makes and publishes it, then every device checks the epoch's recipients against the members' devices and fetches its wrapped key. In the window each send seals its text with AES-256-GCM under the conversation's epoch key and each receiving device opens it; a message that does not open counts in `decrypt_errors` and not as received. The load therefore includes the cryptography on the load generator's side (one AES-GCM operation per send and per receipt; key agreement only before the warm-up) and, on the backend's side, sealed content in place of text (the 4-character text becomes 22 bytes of ciphertext with a 12-byte nonce) and the epoch check on every send, but no key requests in the window unless a device signs in or out or a group's membership changes. Results from before encryption sent plain text.

`--attachment-share` (0 to 1, default 0) gives that share of the sends, chosen at random per send, an attachment of `--attachment-size` random bytes (default 51200, up to 25 MiB less 28). The sending device encrypts it with a new key, as the clients do, and uploads the blob through the blob proxy (the blob address derived from `--url`, port 9200 for `ws`) before sealing and sending the message, which carries the blob's id, key and digest; the send time, and so the end-to-end latency and the send round trip, start after the upload. Every receiving device downloads the blob, checks its digest and decrypts it, after recording the message's receipt; the grace after the window also waits for downloads in progress. The results record `attachment_share`, `attachment_size`, and, for messages sent in the window, `uploads` and `downloads`, with `upload_errors` and `download_errors` (those count every failure; a failed upload sends nothing). The load then includes the blob storage behind the proxy, and the backend's upload checks on each such send (the sender's upload rows, a HEAD of each blob, and marking them used). With the default share of 0 nothing is uploaded and runs are as before. The protocol below uses no attachments.

1. **Idle connections.** 500, 1000, 2000 and 4000 devices, one per user, no messages. Warm-up 10 s, then 30 s. Memory per connection is the slope over the connection count, from a least-squares fit across the four points, which cancels the process baseline; report the baseline too. The load generator samples both resident memory and the runtime's live memory after asking the backend to collect garbage (`POST /debug/gc`, which backends answer only on the host, so the load generator runs beside them); quote the live slope, with the RSS slope beside it.
2. **Latency at fixed rates.** 1000 devices, one per user, at 100, 500, 1000, 1500 and 2000 messages per second (results before 2026-10-22 have no 1500 step). Warm-up 15 s, then 60 s. Report end-to-end p50 and p99 (send on one device to receipt on the peer's device), the send round trip p50, CPU time per thousand messages, and the share of messages received within the 10 s grace after the window.
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
- The database and brokers are capped in memory and run under Docker Desktop's virtual machine; they may saturate before a backend does, and on a laptop they do, between 1000 and 1500 messages per second (between 500 and 1000 before 2026-10-21) (ROADMAP.md, "Database ceiling"). Watch their CPU with `make infra-status` during the high-rate runs and note it.
