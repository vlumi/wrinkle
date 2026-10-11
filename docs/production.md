# Toward production

What it would take to run Wrinkle on the public internet for real users, and what a minimal deployment would cost. Wrinkle is a prototype for comparing implementations, so this is a roadmap, not a plan anyone is following.

## What has to change first

In rough order; each step assumes the ones before it.

### 1. Hardening

Done, without authentication or encryption (ROADMAP.md, "Hardening"): limits on frames, requests and rates per connection, a deadline for Connect, keepalive and idle timeouts, close codes, presence that expires with its gateway, outbox rows deleted once streamed, and gateways that drain on shutdown. Still open for a public service: limits per user across devices and per address.

### 2. Encryption in transit

- **Devices to the edge**: TLS at the device balancer, `wss://` only, certificates from Let's Encrypt (HAProxy or Caddy renew them; a cloud load balancer manages its own). HTTP Strict Transport Security for the web client.
- **Inside**: start on a private network that only the edge is exposed from; then TLS with client certificates (mTLS) between gateways, services and the infrastructure, which all support it.
- **At rest**: encrypted disks and backups. Managed databases do it by default; on own hardware, full-disk encryption.
- **End to end**: a separate feature with consequences for the whole design, [#6](https://github.com/vlumi/wrinkle/issues/6).

### 3. Authentication

Today anyone can sign in as anyone by typing a name. In its place:

- **Accounts** with stable ids and changeable handles ([#1](https://github.com/vlumi/wrinkle/issues/1)).
- **Sign-in** with passkeys (WebAuthn, supported by both the browsers and iOS) as the main method, or an identity provider over OpenID Connect. An iOS app that offers third-party sign-in must offer Sign in with Apple too.
- **Sessions per device**: sign-in over HTTPS issues a refresh token kept by the device and short-lived signed access tokens. `ConnectRequest` carries the access token instead of a name; a gateway verifies its signature without a database read. Signing a device out revokes its refresh token and closes its connection.
- **Never a token in the URL**: the first frame already has to be the connect request, which keeps tokens out of logs. Browsers' WebSocket handshakes are checked against an allowed `Origin`.
- Sign-in is rate limited per account and per address.

### 4. Abuse and moderation

The services already refuse requests about conversations the user is not in. Missing for an open service:

- Blocking a user, and reporting a message or a user, with someone to read the reports. App Store review requires both of apps with user-generated content, and account deletion of apps with accounts.
- Limits on new accounts and on conversations opened with strangers, against spam.

### 5. Data

- Backups taken automatically, kept off the machine, and restored on a schedule to prove they work.
- A retention policy: the capacity estimates assume two weeks of history on the server; the prototype keeps everything.
- Privacy obligations where users are in the EU (GDPR): a privacy policy, export and deletion of a user's data (including the copies in partners' logs and in backups as they age out), data processing agreements with the hosting providers, and an EU region.

### 6. Operations

- One implementation in production. Go is the only one whose fan-out sends push notifications; a Rust gateway in front of Go services is the leanest pairing measured.
- Images built and pushed by CI to a registry, tagged by commit; deploys by tag, rolling one gateway at a time.
- Secrets (database credentials, the production APNs key) in the platform's secret store, not in files on the host.
- Metrics scraped from every process, alerts on error rates, latency, consumer lag and disk, logs collected, and an outside uptime check.
- Schema changes applied as versioned migrations, compatible with the running version, not loaded at startup.
- The change-data path's fragility on a Vitess restart (ROADMAP.md, "Later") resolved, or Vitess replaced as below.

### 7. Clients

- The web client served as static files from a CDN or object storage, or from the edge host.
- The iOS app on TestFlight, then the App Store, using APNs's production environment, with privacy labels and the account deletion above.

## Simplifying the infrastructure

The prototype's infrastructure is shaped for the scale the design was sized for ([capacity.md](capacity.md)), not for a first deployment:

- **Vitess**: managed clouds offer plain MySQL, not Vitess. The schema runs unsharded on one MySQL; Debezium would use its MySQL connector, reading the binlog, instead of the Vitess one. Managed Vitess exists (PlanetScale, on AWS or GCP) and keeps the sharding path open.
- **Kafka**: managed Kafka has a high floor (below). A single broker, or Redpanda as one binary, carries a small service; it is the simplest piece to move to a managed service later.
- **Valkey** is cheap everywhere.

## Deployment options

Prices are list prices read in October 2026, mostly from third-party price trackers, without tax, outbound traffic or support plans; check the providers' calculators before committing. Hetzner raised its cloud prices twice in 2026 (April and June) because of memory costs, and more may follow. The figures are monthly.

Every option needs a domain (about €1 a month) and an Apple developer account for push and distribution (99 USD a year), whatever the hosting.

### A. One server, everything in containers

The cluster's Compose files on one machine, HAProxy terminating TLS, nightly backups to storage elsewhere. The whole stack needs about 8 GB as measured locally; 16 GB leaves room.

| Item | Cost |
|---|---|
| Hetzner CAX31 (8 Arm vCPUs, 16 GB, 160 GB disk) | €21 |
| Hetzner server backups (20 % of the server) | €4 |
| Off-site backup storage (Hetzner Storage Box, 1 TB) | about €4 |
| **Total** | **about €30** |

Memory is what the stack runs out of first, so high-memory plans elsewhere fit too: Linode's (Akamai) 24 GB plan with 2 shared vCPUs is $60 a month. Its 20 GB disk is too small for the images, the database and Kafka's log, so it needs a block storage volume besides (about $10 a month for 100 GB). Two shared vCPUs are plenty for what a non-commercial service would see, a few messages a second; they would only be tight near the hundreds a second the measurements run at. Hetzner's offer is less than half the price, with a third less memory.

The arm64 images the prototype builds for Apple Silicon run unchanged on Hetzner's Arm servers; on x86 hosts such as Linode they build the same way. No redundancy: a failed server is downtime until it is restored from backup. Enough for friends and family, and the right first step for anything.

### B. Three servers

Three of the same servers behind Hetzner's load balancer: gateways and services on each, Kafka as a three-broker cluster, MySQL with a replica, Valkey with a replica. Survives losing a machine, after real work: a replicated database (a primary and replica MySQL, or a real Vitess topology, both still open items) and failover for each piece.

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

Pays back against option A in two to three years, but the home connection and power are the availability, and Cloudflare terminates TLS, so it sees the traffic, unlike option A. Until end-to-end encryption exists ([#6](https://github.com/vlumi/wrinkle/issues/6)) that includes message text.

### D. AWS

Two ways, by how much is managed. us-east-1 prices; Frankfurt is about 10 to 20 % more.

| Item | Mostly managed | Managed where cheap |
|---|---|---|
| Gateways and services: 2 × EC2 t4g.medium (2 vCPUs, 4 GB) | $49 | $49 |
| Database: RDS MySQL db.t4g.medium, one zone, 50 GB | $53 | $53 |
| Kafka | MSK Serverless, about $580 with partitions | one broker and Debezium on a t4g.medium, $25 |
| Debezium | MSK Connect, about $80 | (on the broker's instance) |
| Valkey: ElastiCache Serverless | about $10 | about $10 |
| Network Load Balancer | about $25 | about $25 |
| Public IPv4 addresses ($3.65 each) | about $11 | about $11 |
| **Total** | **about $810** | **about $175** |

Managed Kafka is three quarters of the first column, at a traffic level it will not notice. Keep instances in public subnets or behind IPv6: a NAT gateway adds about $33 a month plus traffic. Outbound traffic past the first 100 GB a month costs $0.09 per GB. PlanetScale's smallest Vitess cluster is listed by trackers at about $39 a month in place of RDS, which keeps the sharded schema and Debezium's Vitess connector; its own price page has the current figure.

### E. Google Cloud

| Item | Mostly managed | Managed where cheap |
|---|---|---|
| Gateways and services: 2 × e2-medium (2 vCPUs, 4 GB) | $49 | $49 |
| Database: Cloud SQL for MySQL, db-standard-1, 50 GB SSD | $58 | $58 |
| Kafka: Managed Service for Apache Kafka, the minimum of 3 vCPUs and 3 GiB | about $140 plus storage | one broker and Debezium on an e2-medium, $25 |
| Debezium | on one of the VMs | (on the broker's instance) |
| Valkey: Memorystore, the smallest node | about $23 | about $23 |
| External network load balancer, one forwarding rule | about $18 | about $18 |
| **Total** | **about $300** | **about $175** |

Google's managed Kafka has a lower floor than AWS's. Cloud Run would host the gateways without VMs, holding WebSockets up to its 60-minute request limit, after which clients reconnect; with instances kept warm for the connections it saves little at this size.

### Which one

Without a commercial launch, traffic stays at a few messages a second, far below what one server handles; the measurements' hundreds a second would take a funded service to reach. So option A first: €30 a month, the same containers as the local cluster, and the work above is the same whichever host comes later. Move to B or a cloud when downtime starts to matter, and to managed Kafka only when traffic justifies its floor.

## Sources

Prices as listed on 2026-10-11:

- Hetzner after the 2026 increases: [Northflank](https://northflank.com/blog/hetzner-cloud-server-price-increases), [AgentDeals](https://agentdeals.dev/hetzner-pricing-2026), [CostGoat](https://costgoat.com/pricing/hetzner)
- AWS: [EC2 t4g.medium](https://calculator.holori.com/aws/ec2/t4g.medium/us-east-1), [RDS db.t4g.medium](https://www.bytebase.com/dbcost/rds/instance/db.t4g.medium/), [MSK](https://www.automq.com/blog/aws-msk-pricing-provisioned-serverless-express), [ElastiCache](https://upstash.com/blog/aws-elasticache-pricing-explained-2026-full-cost-breakdown)
- Google Cloud: [Managed Kafka pricing](https://cloud.google.com/managed-kafka/pricing) and [cluster sizing](https://docs.cloud.google.com/managed-service-for-apache-kafka/docs/plan-cluster-size), [Cloud SQL](https://www.bytebase.com/dbcost/cloudsql-pricing/), [Memorystore for Valkey](https://cloud.google.com/memorystore/docs/valkey/pricing)
- Managed alternatives: [Aiven Kafka](https://aiven.io/pricing/kafka), [PlanetScale](https://agentdeals.dev/vendor/planetscale)
