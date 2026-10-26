# Toward production

What it would take to run Wrinkle on the public internet for real users, and what a minimal deployment would cost. Wrinkle is a prototype for comparing implementations, so this is a roadmap, not a plan anyone is following.

## What has to change first

In rough order; each step assumes the ones before it.

### 1. Hardening

Done (ROADMAP.md, Done, "Hardening"): limits on frames, requests and rates per connection, a deadline for Connect, keepalive and idle timeouts, close codes, presence that expires with its gateway, outbox rows deleted in the transaction that inserts them (the binlog carries the event, the table stays empty), and gateways that drain on shutdown. Still open for a public service: limits per user across devices and per address.

### 2. Encryption in transit

- **Devices to the edge**: TLS at the device balancer, `wss://` only, certificates from Let's Encrypt (HAProxy or Caddy renew them; a cloud load balancer manages its own). HTTP Strict Transport Security for the web client. Locally this is already in place, with a certificate from mkcert ([local-network.md](local-network.md)).
- **Inside**: start on a private network that only the edge is exposed from; then TLS with client certificates (mTLS) between gateways, services and the infrastructure, which all support it.
- **At rest**: encrypted disks and backups. Managed databases do it by default; on own hardware, full-disk encryption.
- **End to end**: done ([architecture](architecture.md#end-to-end-encryption)): a key per conversation and epoch, wrapped for every member device, with the services relaying only ciphertext. What a public service would add is forward secrecy between rotations, which MLS (RFC 9420) gives behind the same requests.

### 3. Authentication

Done: accounts with random ids and changeable names, passwords hashed with argon2id, and a token per device that Connect carries ([architecture](architecture.md#accounts)). For a public service:

- **Sign-in** with passkeys (WebAuthn, supported by both the browsers and iOS) as the main method, or an identity provider over OpenID Connect. An iOS app that offers third-party sign-in must offer Sign in with Apple too.
- **Sessions per device**: the device tokens could become refresh tokens, with short-lived signed access tokens that a gateway verifies without a database read. Signing a device out already closes its open connections.
- **Password recovery** by email, if passwords stay. Account deletion is done.
- **Never a token in the URL**: the first frame already has to be the connect request, which keeps tokens out of logs. Browsers' WebSocket handshakes are checked against an allowed `Origin`.
- Sign-in is rate limited per account and per address.

### 4. Abuse and moderation

The services already refuse requests about conversations the user is not in. Missing for an open service:

- Reporting a message or a user, with someone to read the reports. Blocking is done ([architecture](architecture.md#contacts-and-blocking)). App Store review requires both of apps with user-generated content, and account deletion of apps with accounts.
- Limits on new accounts and on conversations opened with strangers, against spam.

### 5. Data

- Object storage from the provider (S3, GCS or another S3-compatible service) in place of the local Garage node, with blobs served through a CDN; uploads stay signed by the services.
- Backups taken automatically, kept off the machine, and restored on a schedule to prove they work.
- A retention policy. The user logs are trimmed after 30 days (`WRINKLE_LOG_RETENTION_S`); history is kept for good, the last 30 days in MySQL (`WRINKLE_HISTORY_HOT_S`) and older messages in HBase ([architecture](architecture.md#history-and-the-cold-tier)). How long a public service keeps history and attachment blobs is a decision left for it (ROADMAP.md, Open, 2. Retention and history in cold storage).
- Privacy obligations where users are in the EU (GDPR): a privacy policy, export and deletion of a user's data (including the copies in partners' logs, the history in the HBase cold tier, and backups as they age out; a deleted account's messages stay with their conversations, as the prototype keeps them), data processing agreements with the hosting providers, and an EU region.

### 6. Operations

- One implementation in production. A Rust gateway in front of Go services is the leanest pairing measured.
- Images built and pushed by CI to a registry, tagged by commit; deploys by tag, rolling one gateway at a time.
- Secrets (database credentials, the production APNs key) in the platform's secret store, not in files on the host.
- Metrics scraped from every process, alerts on error rates, latency, consumer lag and disk, logs collected, and an outside uptime check.
- Schema changes applied as versioned migrations, compatible with the running version, not loaded at startup.
- The prototype's Vitess test server, which starts empty when its container restarts ([local-network.md](local-network.md#troubleshooting)), replaced by a real Vitess topology or by plain MySQL as below, whose binlog survives a restart.

### 7. Clients

- The web client served as static files from a CDN or object storage, or from the edge host, with a strict Content-Security-Policy and Subresource Integrity: its end-to-end encryption is only as strong as whoever serves its code, which signed, reproducible builds would let users check.
- The iOS app on TestFlight, then the App Store, using APNs's production environment, with privacy labels and the account deletion above.

## Simplifying the infrastructure

The prototype's infrastructure is shaped for the scale the design was sized for ([architecture.md, Choices](architecture.md#choices); the figures in [capacity.md](capacity.md)), not for a first deployment:

- **Vitess**: managed clouds offer plain MySQL, not Vitess. The schema runs unsharded on one MySQL; Debezium uses its MySQL connector, reading the binlog, instead of the Vitess one, as the infrastructure's `mysql` profile does locally (`make infra-up PROFILE=mysql`). Managed Vitess exists (PlanetScale, on AWS or GCP) and keeps the sharding path open.
- **Kafka**: managed Kafka has a high floor (below). A single broker, or Redpanda as one binary, carries a small service; it is the simplest piece to move to a managed service later.
- **HBase**, the cold tier of the history: Bigtable speaks the HBase API, the managed path on Google Cloud; elsewhere HBase self-hosted, standalone at first, or on Amazon EMR. Its REST server is needed only for the TypeScript backend.
- **Valkey** is cheap everywhere. Besides presence, it holds the leases that share the per-shard background work, the log trimmer and the history mover, between instances: without it no logs are trimmed and no history moves.

## Deployment options

Prices are list prices read in October 2026, mostly from third-party price trackers, without tax, outbound traffic or support plans; check the providers' calculators before committing. Hetzner raised its cloud prices twice in 2026 (April and June) because of memory costs. The figures are monthly.

Every option needs a domain (about €1 a month) and an Apple developer account for push and distribution (99 USD a year), whatever the hosting.

### A. One server, everything in containers

The cluster's Compose files on one machine, HAProxy terminating TLS, nightly backups to storage elsewhere. Locally the infrastructure's caps add up to about 6.4 GB, HBase and its REST server about 1.4 GB of it, and the cluster with the backends runs in 12 GB of Docker memory; 16 GB leaves room. Locally neither the database nor HBase keeps its data in a volume; on a server both need one, and the backups must cover HBase as well as MySQL.

| Item | Cost |
|---|---|
| Hetzner CAX31 (8 Arm vCPUs, 16 GB, 160 GB disk) | €21 |
| Hetzner server backups (20 % of the server) | €4 |
| Off-site backup storage (Hetzner Storage Box, 1 TB) | about €4 |
| **Total** | **about €30** |

Memory is what the stack runs out of first, so high-memory plans elsewhere fit too: Linode's (Akamai) 24 GB plan with 2 shared vCPUs is $60 a month. Its 20 GB disk is too small for the images, the database and Kafka's log, so it needs a block storage volume besides (about $10 a month for 100 GB). Two shared vCPUs are plenty for what a non-commercial service would see, a few messages a second; they would only be tight near the hundreds a second the measurements run at. Hetzner's offer is less than half the price, with a third less memory.

The arm64 images the prototype builds for Apple Silicon run unchanged on Hetzner's Arm servers; on x86 hosts such as Linode they build the same way. No redundancy: a failed server is downtime until it is restored from backup. Enough for friends and family, and the right first step for anything.

### B. Three servers

Three of the same servers behind Hetzner's load balancer: gateways and services on each, Kafka as a three-broker cluster, MySQL with a replica, Valkey with a replica, and HBase: standalone on one of them, sized around the local cap of about 1.4 GB with its REST server, or a replicated cluster on HDFS. Survives losing a machine, after real work: a replicated database (a primary and replica MySQL, or a real Vitess topology, both still open items) and failover for each piece.

| Item | Cost |
|---|---|
| 3 × Hetzner CAX31 | €63 |
| Hetzner Load Balancer LB11 | about €7 |
| Backups and off-site storage | about €17 |
| **Total** | **about €90** |

### C. Own hardware

A small computer at home or in an office, reached through Cloudflare Tunnel (free, carries WebSockets, no open ports or public address needed), or through a forwarded port with Caddy and Let's Encrypt.

| Item | Cost |
|---|---|
| Mini PC with 32 GB memory and 1 TB NVMe, once | about €600 to 900 |
| Electricity, 15 to 30 W around the clock at €0.25/kWh | €3 to 6 |
| Off-site backup storage | about €4 |
| **Monthly** | **about €10**, plus the hardware |

Pays back against option A in two to three years, but the home connection and power are the availability, and Cloudflare terminates TLS, so it sees the traffic, unlike option A: not message content, which is encrypted end to end, but who talks to whom, profiles and group topics.

### D. AWS

Two ways, by how much is managed. us-east-1 prices; Frankfurt is about 10 to 20 % more.

| Item | Mostly managed | Managed where cheap |
|---|---|---|
| Gateways and services: 2 × EC2 t4g.medium (2 vCPUs, 4 GB) | $49 | $49 |
| Database: RDS MySQL db.t4g.medium, one zone, 50 GB | $53 | $53 |
| Kafka | MSK Serverless, about $580 with partitions | one broker and Debezium on a t4g.medium, $25 |
| Debezium | MSK Connect, about $80 | (on the broker's instance) |
| Valkey: ElastiCache Serverless | about $10 | about $10 |
| Cold tier: HBase and its REST server on a t4g.medium | $25 | $25 |
| Network Load Balancer | about $25 | about $25 |
| Public IPv4 addresses ($3.65 each) | about $11 | about $11 |
| **Total** | **about $835** | **about $200** |

AWS has no managed HBase short of an EMR cluster, so both columns run it on an instance. Managed Kafka is three quarters of the first column, at a traffic level it will not notice. Keep instances in public subnets or behind IPv6: a NAT gateway adds about $33 a month plus traffic. Outbound traffic past the first 100 GB a month costs $0.09 per GB. PlanetScale's smallest Vitess cluster is listed by trackers at about $39 a month in place of RDS, which keeps the sharded schema and Debezium's Vitess connector; its own price page has the current figure.

### E. Google Cloud

| Item | Mostly managed | Managed where cheap |
|---|---|---|
| Gateways and services: 2 × e2-medium (2 vCPUs, 4 GB) | $49 | $49 |
| Database: Cloud SQL for MySQL, db-standard-1, 50 GB SSD | $58 | $58 |
| Kafka: Managed Service for Apache Kafka, the minimum of 3 vCPUs and 3 GiB | about $140 plus storage | one broker and Debezium on an e2-medium, $25 |
| Debezium | on one of the VMs | (on the broker's instance) |
| Valkey: Memorystore, the smallest node | about $23 | about $23 |
| Cold tier | Bigtable, one node, billed by the node-hour (not priced here) | HBase and its REST server on an e2-medium, $25 |
| External network load balancer, one forwarding rule | about $18 | about $18 |
| **Total** | **about $300** plus Bigtable | **about $200** |

Google's managed Kafka has a lower floor than AWS's. Bigtable bills at least one node around the clock, a floor of its own at this traffic; its price page has the current figure. Cloud Run would host the gateways without VMs, holding WebSockets up to its 60-minute request limit, after which clients reconnect; with instances kept warm for the connections it saves little at this size.

### Which one

Without a commercial launch, traffic stays at a few messages a second, far below what one server handles; the measurements' hundreds a second would take a funded service to reach. So option A first: €30 a month, the same containers as the local cluster, and the work above is the same whichever host comes later. Move to B or a cloud when downtime starts to matter, and to managed Kafka only when traffic justifies its floor.

## Sources

Prices as listed on 2026-10-11:

- Hetzner after the 2026 increases: [Northflank](https://northflank.com/blog/hetzner-cloud-server-price-increases), [AgentDeals](https://agentdeals.dev/hetzner-pricing-2026), [CostGoat](https://costgoat.com/pricing/hetzner)
- AWS: [EC2 t4g.medium](https://calculator.holori.com/aws/ec2/t4g.medium/us-east-1), [RDS db.t4g.medium](https://www.bytebase.com/dbcost/rds/instance/db.t4g.medium/), [MSK](https://www.automq.com/blog/aws-msk-pricing-provisioned-serverless-express), [ElastiCache](https://upstash.com/blog/aws-elasticache-pricing-explained-2026-full-cost-breakdown)
- Google Cloud: [Managed Kafka pricing](https://cloud.google.com/managed-kafka/pricing) and [cluster sizing](https://docs.cloud.google.com/managed-service-for-apache-kafka/docs/plan-cluster-size), [Cloud SQL](https://www.bytebase.com/dbcost/cloudsql-pricing/), [Memorystore for Valkey](https://cloud.google.com/memorystore/docs/valkey/pricing)
- Managed alternatives: [Aiven Kafka](https://aiven.io/pricing/kafka), [PlanetScale](https://agentdeals.dev/vendor/planetscale)
