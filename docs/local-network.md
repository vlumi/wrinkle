# Running on a local network

Run the stack and the backends on a Mac, then connect to them from phones and other computers on the same network for manual testing. The steps assume a fresh Apple-silicon Mac with Homebrew. The quickest start needs only Docker: the infrastructure and the [cluster](#backends) build and run in containers. The native backends need each language's toolchain, below.

## The path

Before the first run: install Docker Desktop and give it at least 8 GB of memory, 12 GB for the cluster (Settings, Resources): the stack's memory caps in `infra/docker-compose.yml` add up to about 6.4 GB, and the cluster adds nine backend containers by default, without caps; `brew install git mkcert`; clone the repository (while it is private, the Mac needs an SSH key or token that GitHub accepts); and keep the Mac awake (System Settings, Energy, "Prevent automatic sleeping when the display is off"). The generated code is committed, so the code generators (`make tools`) are not needed to run anything. Then, top to bottom:

1. `mkcert -install`, once: makes a local certificate authority and trusts it on this Mac; asks for your password.
2. `make certs`: a certificate for this Mac's names and addresses, into `infra/certs`; again when the LAN address changes.
3. `make infra-up`: the shared stack. The first run builds the arm64 Vitess image from source and the HBase image from the 2.6.7 release, which it downloads, and takes a few minutes; later runs start in about 20 seconds. Each run creates the HBase table if it is missing. `make infra-up PROFILE=mysql` runs plain MySQL in place of Vitess and builds no Vitess image, so its first run is faster. `make infra-status` shows how it is doing.
4. `make cluster-up`: the whole backend in containers, serving devices on loopback port 8080. A native backend instead: `make run-go-mono`, `make run-java-mono` or `make run-ts-mono` ([Backends](#backends)).
5. `make web`: the web client on port 8081, over HTTPS with the certificate, at `https://<address>:8081/`, and with it the TLS edge on port 8443 (`make tls-up`, which `make web` runs first; running it again changes nothing), in front of whatever serves port 8080. Devices connect to `wss://<address>:8443/v1/ws` and read and upload blobs at `https://<address>:8443/blobs/` on the same edge; port 8080 itself listens on loopback only, for the clients, tests and load runs on the Mac.
6. Trust the certificate on each device, once ([below](#trusting-the-certificate)), and open `https://<address>:8081/` on it. The address is the Mac's Bonjour name or its LAN IP:

   ```sh
   echo "$(scutil --get LocalHostName).local"
   ipconfig getifaddr en0
   ```

`make tls-down` stops the edge, `make cluster-down` the cluster, `make infra-down` the stack, discarding the data; Ctrl-C stops a native process.

## Trusting the certificate

Each device that connects to the TLS edge or opens the web client must trust mkcert's local authority once, since devices send passwords and the edge needs a certificate they accept. Its root certificate is `rootCA.pem` in the directory `mkcert -CAROOT` prints; it is not secret, but its key, beside it, is: never copy `rootCA-key.pem` anywhere.

- **iPhone or iPad:** AirDrop or mail `rootCA.pem` to the device and open it; install the downloaded profile in Settings, General, VPN & Device Management. Then turn on full trust for it in Settings, General, About, Certificate Trust Settings.
- **Another Mac:** open `rootCA.pem` in Keychain Access, then set it to Always Trust.
- **Android:** Settings, Security, Encryption & credentials, Install a certificate, CA certificate.
- **The simulator** shares the host's network and connects to `ws://localhost:8080/v1/ws` without TLS.

## The web client

A React app built to static files and served by a small Node server; it needs Node 22 or later (`brew install node`). `make web` installs the dependencies, builds the app, and serves it on port 8081 to the network, over HTTPS with the certificate from `make certs`, after starting the TLS edge. Without a certificate the server refuses to start unless told to serve only the Mac itself (`HOST=127.0.0.1`), as the browser test does. The gateway field defaults to the TLS edge on the same host, `wss://<address>:8443/v1/ws`; on the Mac itself, `ws://localhost:8080/v1/ws` works too. Every gateway the browser has signed in through is listed under the field, most recent first: tap one to fill the field, or × to forget it. On a phone it shows one pane at a time, the conversations and then a conversation with a back button; on a wider screen both side by side. Create an account or sign in with a name and password; the browser keeps its device token and reconnects with it. Your own name at the top opens the settings: name, icon or picture, password, the account's devices (rename, or sign out another one, which closes its connection at once), sign out and delete account. Each browser is a separate device; a private window is a quick second one.

For working on the client itself, `make web-dev` serves it with live reload on the same port, over HTTPS and with the edge likewise.

## The iOS app

### In the simulator

On the host itself, with Xcode 26:

```sh
brew install xcodegen
make ios-sim
```

The simulator shares the host's network, so the default `ws://localhost:8080/v1/ws` works. Create an account on the sign-in screen, or sign in to one made elsewhere; the app keeps its device token in the Keychain and connects with it from then on. Your avatar at the top of the conversation list opens the profile: name, icon, password and sign out.

### On an iPhone

Needs an Apple developer team. Copy `.env.example` to `.env` at the repository root and set `WRINKLE_TEAM_ID`; the project generator signs with it. Turn on Developer Mode on the phone (Settings, Privacy & Security), connect it by cable once and trust the Mac, then:

```sh
brew install xcodegen
make ios-device
```

That builds, installs and launches the app; with several phones paired, pick one with `DEVICE=<part of its name>`. The first build registers the app id and a development profile through Xcode's automatic signing, so the Mac's Xcode must be signed in to the team's account (Xcode, Settings, Accounts). On the phone, the app asks to find devices on the local network and to send notifications; allow both, then enter `wss://<address>:8443/v1/ws` on the sign-in screen, with a name and password. The phone must trust the local authority first ([above](#trusting-the-certificate)).

## Push notifications

The app registers for push and sends its token to the backend after every connect. While the app is in the background its connection is closed, and the backend's fan-out pushes new messages to it through APNs instead, with the unread count as badge. Tapping a notification opens the conversation.

Push needs an APNs key: in the developer account, Certificates, Identifiers & Profiles, Keys, create a key with "Apple Push Notifications service" enabled and download it (`AuthKey_<key id>.p8`). An App Store Connect API key is a different kind and does not work. Then in `.env`:

```sh
WRINKLE_TEAM_ID=ABCDE12345
WRINKLE_APNS_KEY=~/path/to/AuthKey_XXXXXXXXXX.p8
```

and restart the backend; it logs "push notifications on". Without a key everything else works and the app catches up when it is opened.

Pushes are sent by the fan-out of whichever monolith or services instances run, Go, Java or TypeScript; which gateway a device uses does not matter. In a pool, the instances split Kafka's partitions between them, so each sends the pushes for the conversations it consumes, and every one of them needs the key: the cluster and the side-by-side targets read it from `.env` for all of them.

Message text is encrypted, so the backend sends notifications with the generic body "New message" and the sealed content; the app's notification service extension decrypts it on the phone. It reads the keys from an app group, `group.fi.misaki.wrinkle`, which the team must have and both app ids (`fi.misaki.wrinkle` and `fi.misaki.wrinkle.NotificationService`) must be allowed; automatic signing in `make ios-device` usually registers it, otherwise add App Groups under Signing & Capabilities in Xcode for both targets. Without it the app works, but notifications keep the generic body.

To try the app's handling of a notification without APNs, the simulator takes a payload directly. This is what the backend sends for a message, minus the sealed content (`epoch`, `nonce`, `ciphertext`, `sender_user_id`, `client_message_id`), so the extension leaves the generic body, as it does for a message too large to carry; `conversation_id` is the lowercase hex of a conversation's id, 32 characters, and tapping the notification opens that conversation if the app has it:

```sh
id=0123456789abcdef0123456789abcdef
echo '{"aps":{"alert":{"title":"bob","body":"New message"},"badge":1,"sound":"default","thread-id":"'$id'","mutable-content":1},"conversation_id":"'$id'","seq":1}' > push.json
xcrun simctl push booted fi.misaki.wrinkle push.json
```

A deletion is a background notification, `{"aps":{"content-available":1},"conversation_id":"<id>","seq":1,"deleted":true}`, on which the app withdraws the message's notification if it showed one.

## Several Macs running the stack

Each Mac's `mkcert -install` would make its own authority, and every device would have to trust each one. Instead, make the authority on one Mac and copy it to the others before setting them up, so devices trust one root for all of them:

```sh
# On the first Mac: where the authority is
mkcert -CAROOT
# Copy that directory's rootCA.pem and rootCA-key.pem to the same path on the
# other Mac (mkcert -CAROOT there prints it), by AirDrop or scp, then there:
mkcert -install
make certs
make tls-up
```

`rootCA-key.pem` lets anyone holding it make certificates your devices trust: copy it only between your own machines, and never into the repository. A Mac that only connects to another, through a browser or a client, needs just `rootCA.pem` in its Keychain, set to Always Trust, as above; not `mkcert -install`.

## Backends

Three ways to run them, all against the same database, so users and conversations carry over between them: the cluster, a monolith, or the side-by-side setup. Each holds port 8080, so one of them at a time. `make` lists every target.

### Cluster

The side-by-side setup in containers, from one command: the balancer, the services instances and the gateways, each in any number, on the infrastructure's network. The first run builds an image per backend, a few minutes; later runs take seconds.

```sh
make cluster-up                                  # 2 Go, 1 Java and 1 TypeScript services, one gateway of each of the five languages
make cluster-up GO_GW=3 RUST_GW=0 JAVA_SERVICES=0  # rescales in place; counts not given stay as they are
make cluster-status                              # every container
make cluster-logs SERVICE=go-gw                  # follow one kind, or all without SERVICE
make cluster-down                                # remove the cluster; the infrastructure keeps running
```

The counts are `GO_SERVICES`, `JAVA_SERVICES`, `TS_SERVICES`, `GO_GW`, `ELIXIR_GW`, `RUST_GW`, `JAVA_GW` and `TS_GW`, 0 to 8 each. A count not given keeps what is running, so after a code change a plain `make cluster-up` rebuilds the images and replaces the containers whose image changed, at the same counts. The defaults apply on a fresh start, after `cluster-down`. Devices connect through the TLS edge as before, and `http://localhost:8404/` shows every replica the balancer has found. The balancer finds them by name through Docker's DNS: a kind scaled up joins within a few seconds, one scaled down drops out. Push settings come from `.env` as for the other targets.

The cluster is for trying the scaled shape, not for measuring: the containers run in Docker Desktop's virtual machine, behind its networking, so their numbers are not comparable with the native processes the measurements use. Its processes don't show in Grafana.

### Monoliths

`make run-go-mono`, `make run-java-mono` or `make run-ts-mono`: one native process with every role on port 8080, on loopback. Go needs `brew install go`; TypeScript Node 26 or later (the web client needs only 22); Java the Homebrew JDK, which the Makefile finds through `java_home` once the symlink exists:

```sh
brew install openjdk@25
mkdir -p ~/Library/Java/JavaVirtualMachines
ln -sfn /opt/homebrew/opt/openjdk@25/libexec/openjdk.jdk ~/Library/Java/JavaVirtualMachines/openjdk-25.jdk
```

The first Java build downloads Gradle and the dependencies; the first TypeScript one, `make ts-deps`, compiles the Kafka client's librdkafka, which takes minutes.

The first time each program listens, macOS asks whether to accept incoming connections; allow it. If the prompt was missed, add the program under System Settings, Network, Firewall, Options. Each backend is a different program to the firewall: `bin/wrinkle`, `java`, `node`, `beam.smp` and the Rust binary. The make targets build to fixed paths, so each is asked about once; `go run` would build to a new temporary path every time and ask again.

### Side by side

The shape the design scales in, natively: services instances and gateways as separate processes, any number of each, in any of the languages, behind two balancers. Go, Java and TypeScript services implement the same internal contract, so they serve in one pool; their fan-out shares one Kafka consumer group, so they split that work too. The Elixir and Rust implementations are gateways, run beside any of those services. Their toolchains:

```sh
brew install elixir
mix local.hex --force
mix local.rebar --force

brew install rustup
export PATH="/opt/homebrew/opt/rustup/bin:$PATH"
rustup default stable
```

```sh
make lb-up                         # the balancers; leave them running
make run-go-services               # services instance 1, in another terminal
make run-java-services INSTANCE=2  # another, in Java (instances 1 to 4, in any of the three languages)
make run-ts-services INSTANCE=3    # and one in TypeScript
make run-go-gw                     # gateways, each in its own terminal: ws://localhost:8180/v1/ws
make run-elixir-gw                 # :8181
make run-rust-gw                   # :8182
make run-java-gw                   # :8183
make run-ts-gw                     # :8184
make lb-down                       # when done
```

- **Devices to gateways:** the balancer on port 8080, behind the TLS edge for devices on the network, sends each connection to the healthy gateway with the fewest. Stop a gateway and the balancer marks it down within about two seconds; its devices reconnect and land on the others. The gateways listen on loopback; to reach one in particular from the Mac itself, put `ws://localhost:<port>/v1/ws` in the client's gateway field.
- **Gateways to services:** each gateway keeps one HTTP/2 connection to the services balancer on 127.0.0.1:9100, which hands out each request separately, in turn, to the running instances. Instance *n* serves the RPC on 9100+*n*, on loopback only. Stop an instance and the rest take its requests and its share of the fan-out.

`http://localhost:8404/` on the host shows both balancers: each gateway and instance, its state, and what it has handled. A gateway refuses to start until the balancers and at least one services instance are up. `PORT=...` moves a gateway elsewhere, outside the balancer. The balancer holds port 8080, so stop it before a monolith. Prometheus scrapes only port 8080, so these processes don't show in Grafana; the monitoring page links each one's metrics.

## What faces the network

- Only the TLS edge (8443) and the web client's server (8081) face the network. The backends, the balancer and the cluster serve devices on loopback port 8080, and side-by-side gateways on loopback 8180 to 8184. The infrastructure's ports (MySQL through vtgate without a password, or on the mysql profile MySQL itself as `root` without one; Kafka, Kafka Connect, Valkey, the blob proxy, HBase's ZooKeeper 2181, master 16000, master UI 16010 and region server 16020, HBase's REST server 8086, Prometheus, Grafana) are published on loopback, and the internal RPC between gateway and services listens on loopback too. Through the edge, `/blobs/` takes only what devices do: unsigned reads of one blob, and the uploads a backend presigned; the storage key is made per machine by `make infra-up`, kept in `.env`, and never leaves it.
- Accounts have passwords, stored as argon2id hashes, and sign-in gives each device a token. There is no password recovery: a forgotten password means a new account, or `make infra-reset` for everyone.
- `make monitor`, or `https://localhost:8081/monitor` on the host (`http://` before `make certs`), with `make web` running, links to every monitoring page (Grafana, Prometheus, the balancers' statistics, Kafka Connect, Vitess, each native backend's metrics and profiles) and shows which answer. It is served only to the host itself, since those pages listen on loopback; from another device it is a 404.
- Grafana shows the backend's connections, requests and fan-out while you test. To open it from another device, start the stack with `GRAFANA_BIND=0.0.0.0 make infra-up` and browse to `http://<address>:3000`; it has no login.

## Troubleshooting

- **The Mac's address changed** (another network, a new DHCP lease): the certificate no longer covers it. `make certs`, then `make tls-down` so the edge reads the new certificate when `make web` starts it again, and restart `make web`.
- **No event is delivered**, though sends succeed, and the conformance tests fail at their readiness probe with "no event delivered": the Vitess container restarted (a Docker Desktop restart does it, as does rebuilding the image after a change under `infra/vitess/`). The test server starts empty, while Debezium's stored offsets point into history that no longer exists, so both connectors retry forever (`failed to find a PRIMARY tablet for VStream` in `make infra-logs SERVICE=connect`). `make infra-up` detects this and resets the offsets; the data from before the restart is gone, so sign up again, or `make infra-reset` for a clean stack. The plain MySQL profile (`make infra-up PROFILE=mysql`) keeps its data and binlog across a restart and has no such problem.
- **The web client signs in but cannot send**, with "End-to-end encryption needs a secure page": the page was opened over plain HTTP from another device, where browsers give no WebCrypto. Open it over HTTPS, as `make web` serves it, with the certificate trusted on the device. A page over HTTPS cannot use a plain `ws://` gateway either, except `localhost`; the default `wss://` edge is the one to use.
- **The phone cannot connect** to `wss://<address>:8443/v1/ws`: the authority is not trusted on it yet ([above](#trusting-the-certificate)), the Mac is asleep, or `make tls-up` is not running.
- **`make infra-up` fails with "the stack runs the vitess profile"** (or mysql): the two profiles hold different data, so a stack switches only from empty. `make infra-down`, then `make infra-up PROFILE=...`.
- **HBase or its REST server stays unhealthy**, or a backend cannot reach the region server: clients find it by the name it advertises, `hbase.localhost`, an alias of the container on Docker's network and, on the host, loopback through the macOS system resolver, which maps every `.localhost` name there. A resolver that skips the system's misses it. `make infra-logs SERVICE=hbase` shows whether the region server reported to the master.
- **Accounts and history are gone after the stack was recreated:** HBase, like the database, keeps its data in its container, not in a volume, so `make infra-down`, `make infra-reset` or a rebuilt image discards both, the old history in HBase with the rest.
- **The first `make ts-deps`, or any TypeScript target, sits for minutes**: `npm ci` compiles the Kafka client's librdkafka. Later runs skip it until the lockfile changes.
- **A device away for more than 30 days starts over:** the user logs keep 30 days of events, so its sync cursor fails with `ERROR_CODE_CURSOR_EXPIRED`, and the client lists its conversations and contacts and fetches history again. Nothing is lost; the history stays with the conversations.
