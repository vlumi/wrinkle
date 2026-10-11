# Running on a local network

Run the stack and the backends on a Mac, then connect to them from phones and other computers on the same network for manual testing. The steps assume a fresh Apple-silicon Mac with Homebrew. The quickest start needs only Docker: the infrastructure and the [cluster](#cluster) build and run in containers. The native backends need each language's toolchain, below.

## The host

1. Install Docker Desktop and give it at least 8 GB of memory, 12 GB for the cluster (Settings, Resources). Install git, and Go for the native Go backend:

   ```sh
   brew install git go
   ```

2. Clone the repository. While it is private, the Mac needs an SSH key or token that GitHub accepts.

3. Bring up the infrastructure:

   ```sh
   make infra-up
   ```

   The first run builds the arm64 Vitess image from source and takes a few minutes; later runs start in about 20 seconds. `make infra-status` shows how it is doing, `make infra-down` stops everything and discards the data.

4. Keep the Mac awake: System Settings, Energy, turn on "Prevent automatic sleeping when the display is off".

5. Find the address devices will use, the Bonjour name or the LAN IP:

   ```sh
   echo "$(scutil --get LocalHostName).local"
   ipconfig getifaddr en0
   ```

6. Set up TLS, since devices send passwords. mkcert makes a local certificate authority and a certificate for this Mac's names and addresses:

   ```sh
   brew install mkcert
   mkcert -install    # once: trusts the local authority on this Mac; asks for your password
   make certs         # the certificate, into infra/certs; again when the LAN address changes
   make tls-up        # the TLS edge on port 8443, in front of whatever serves port 8080
   ```

   Devices connect to `wss://<address>:8443/v1/ws`. Port 8080 itself listens on loopback only, for the clients, tests and load runs on the Mac. Each device that connects must trust the local authority once; see [Trusting the certificate](#trusting-the-certificate). `make tls-down` stops the edge.

The generated code is committed, so the code generators (`make tools`) are not needed to run anything.

## Backends

Three ways to run them, all against the same database, so users and conversations carry over between them:

- **A monolith:** `make run-go-mono` or `make run-java-mono`, one process with every role on port 8080, on loopback.
- **Side by side:** services instances and gateways as separate native processes, any number of each, behind two balancers; see [Side by side](#side-by-side).
- **The cluster:** the same in containers, from one command; see [Cluster](#cluster).

Each holds port 8080, so one of them at a time; Ctrl-C stops a native process. `make` lists every target.

The first time each program listens, macOS asks whether to accept incoming connections; allow it. If the prompt was missed, add the program under System Settings, Network, Firewall, Options. Each backend is a different program to the firewall: `bin/wrinkle`, `java`, `beam.smp` and the Rust binary. The make targets build to fixed paths, so each is asked about once; `go run` would build to a new temporary path every time and ask again.

### Go

```sh
make run-go-mono
```

Split, as Go services instances and Go gateways, it runs side by side; see [Side by side](#side-by-side).

### Java

```sh
brew install openjdk@25
mkdir -p ~/Library/Java/JavaVirtualMachines
ln -sfn /opt/homebrew/opt/openjdk@25/libexec/openjdk.jdk ~/Library/Java/JavaVirtualMachines/openjdk-25.jdk

make run-java-mono
```

The symlink lets `java_home` find the Homebrew JDK, which the Makefile uses. The first build downloads Gradle and the dependencies. Split, as Java services instances and Java gateways, it runs side by side like Go.

### Elixir

The Elixir implementation is a gateway, run beside Go or Java services as described in [Side by side](#side-by-side). Its toolchain:

```sh
brew install elixir
mix local.hex --force
mix local.rebar --force
```

### Rust

Also a gateway, run the same way. Its toolchain:

```sh
brew install rustup
export PATH="/opt/homebrew/opt/rustup/bin:$PATH"
rustup default stable
```

### Side by side

The shape the design scales in: services instances and gateways as separate processes, any number of each, in any of the languages, behind two balancers. Go and Java services implement the same internal contract, so they serve in one pool; their fan-out shares one Kafka consumer group, so they split that work too.

```sh
make lb-up                         # the balancers; leave them running
make run-go-services               # services instance 1, in another terminal
make run-java-services INSTANCE=2  # another, in Java (instances 1 to 4, either language)
make run-go-gw                     # gateways, each in its own terminal: ws://localhost:8180/v1/ws
make run-elixir-gw                 # :8181
make run-rust-gw                   # :8182
make run-java-gw                   # :8183
make lb-down                       # when done
```

- **Devices to gateways:** the balancer on port 8080, behind the TLS edge for devices on the network, sends each connection to the healthy gateway with the fewest. Stop a gateway and the balancer marks it down within about two seconds; its devices reconnect and land on the others. The gateways listen on loopback; to reach one in particular from the Mac itself, put `ws://localhost:<port>/v1/ws` in the client's gateway field.
- **Gateways to services:** each gateway keeps one HTTP/2 connection to the services balancer on 127.0.0.1:9100, which hands out each request separately, in turn, to the running instances. Instance *n* serves the RPC on 9100+*n*, on loopback only. Stop an instance and the rest take its requests and its share of the fan-out.

`http://localhost:8404/` on the host shows both balancers: each gateway and instance, its state, and what it has handled. A gateway refuses to start until the balancers and at least one services instance are up. `PORT=...` moves a gateway elsewhere, outside the balancer. The balancer holds port 8080, so stop it before `run-go-mono` or `run-java-mono`. Prometheus scrapes only port 8080, so these processes don't show in Grafana; the monitoring page links each one's metrics.

Java services instances store push tokens but send no pushes yet; see [Push notifications](#push-notifications) for what that means in a mixed pool.

### Cluster

The side-by-side setup in containers, from one command: the balancer, the services instances and the gateways, each in any number, on the infrastructure's network. The first run builds an image per backend, a few minutes; later runs take seconds.

```sh
make cluster-up                                  # 2 Go and 1 Java services, one gateway of each language
make cluster-up GO_GW=3 RUST_GW=0 JAVA_SERVICES=0  # rescales in place; counts not given stay as they are
make cluster-status                              # every container
make cluster-logs SERVICE=go-gw                  # follow one kind, or all without SERVICE
make cluster-down                                # remove the cluster; the infrastructure keeps running
```

The counts are `GO_SERVICES`, `JAVA_SERVICES`, `GO_GW`, `ELIXIR_GW`, `RUST_GW` and `JAVA_GW`, 0 to 8 each. A count not given keeps what is running, so after a code change a plain `make cluster-up` rebuilds the images and replaces the containers whose image changed, at the same counts. The defaults apply on a fresh start, after `cluster-down`. Devices connect through the TLS edge as before, and `http://localhost:8404/` shows every replica the balancer has found. The balancer finds them by name through Docker's DNS: a kind scaled up joins within a few seconds, one scaled down drops out. Push settings come from `.env` as for the other targets.

The cluster is for trying the scaled shape, not for measuring: the containers run in Docker Desktop's virtual machine, behind its networking, so their numbers are not comparable with the native processes the measurements use. The cluster holds port 8080, like the native balancer and the monoliths, so one of them at a time. Its processes don't show in Grafana.

## Clients

### Web client

A React app built to static files and served by a small Node server. It needs Node 22 or later (`brew install node`):

```sh
make web
```

That installs the dependencies, builds the app, and serves it on port 8081 to the network, over HTTPS with the certificate from `make certs`. Open `https://<address>:8081/` on the device. The gateway field defaults to the TLS edge on the same host, `wss://<address>:8443/v1/ws`; on the Mac itself, `ws://localhost:8080/v1/ws` works too. Without a certificate the server falls back to plain HTTP, which only works on the Mac. Every gateway the browser has signed in through is listed under the field, most recent first: tap one to fill the field, or × to forget it. On a phone it shows one pane at a time, the conversations and then a conversation with a back button; on a wider screen both side by side. Create an account or sign in with a name and password; the browser keeps its device token and reconnects with it. Your own name at the top opens the settings: name, icon, password and sign out. Each browser is a separate device; a private window is a quick second one.

For working on the client itself, `make web-dev` serves it with live reload on the same port, over HTTPS likewise.

### iOS client in the simulator

On the host itself, with Xcode 26:

```sh
brew install xcodegen
make ios-sim
```

The simulator shares the host's network, so the default `ws://localhost:8080/v1/ws` works. Create an account on the sign-in screen, or sign in to one made elsewhere; the app keeps its device token in the Keychain and connects with it from then on. Your avatar at the top of the conversation list opens the profile: name, icon, password and sign out.

### iOS client on an iPhone

Needs an Apple developer team. Copy `.env.example` to `.env` at the repository root and set `WRINKLE_TEAM_ID`; the project generator signs with it. Turn on Developer Mode on the phone (Settings, Privacy & Security), connect it by cable once and trust the Mac, then:

```sh
brew install xcodegen
make ios-device
```

That builds, installs and launches the app; with several phones paired, pick one with `DEVICE=<part of its name>`. The first build registers the app id and a development profile through Xcode's automatic signing, so the Mac's Xcode must be signed in to the team's account (Xcode, Settings, Accounts). On the phone, the app asks to find devices on the local network and to send notifications; allow both, then enter `wss://<address>:8443/v1/ws` on the sign-in screen, with a name and password. The phone must trust the local authority first; see below.

### Trusting the certificate

Each device that connects to the TLS edge or opens the web client must trust mkcert's local authority once. Its root certificate is `rootCA.pem` in the directory `mkcert -CAROOT` prints; it is not secret, but its key, beside it, is: never copy `rootCA-key.pem` anywhere.

- **iPhone or iPad:** AirDrop or mail `rootCA.pem` to the device and open it; install the downloaded profile in Settings, General, VPN & Device Management. Then turn on full trust for it in Settings, General, About, Certificate Trust Settings.
- **Another Mac:** open `rootCA.pem` in Keychain Access, then set it to Always Trust.
- **Android:** Settings, Security, Encryption & credentials, Install a certificate, CA certificate.
- **The simulator** shares the host's network and connects to `ws://localhost:8080/v1/ws` without TLS.

**Several Macs running the stack.** Each Mac's `mkcert -install` would make its own authority, and every device would have to trust each one. Instead, make the authority on one Mac and copy it to the others before setting them up, so devices trust one root for all of them:

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

### Push notifications

The app registers for push and sends its token to the backend after every connect. While the app is in the background its connection is closed, and the Go backend's fan-out pushes new messages to it through APNs instead, with the unread count as badge. Tapping a notification opens the conversation.

Push needs an APNs key: in the developer account, Certificates, Identifiers & Profiles, Keys, create a key with "Apple Push Notifications service" enabled and download it (`AuthKey_<key id>.p8`). An App Store Connect API key is a different kind and does not work. Then in `.env`:

```sh
WRINKLE_TEAM_ID=ABCDE12345
WRINKLE_APNS_KEY=~/path/to/AuthKey_XXXXXXXXXX.p8
```

and restart the backend; it logs "push notifications on". Without a key everything else works and the app catches up when it is opened.

Pushes are sent by the Go fan-out, so they need the Go monolith or Go services instances; which gateway a device uses does not matter. The Java fan-out stores tokens but does not push yet. In a pool with both, the Go and Java instances split Kafka's partitions between them, so a message whose conversation falls to a Java instance gets no push; for testing push, run only Go services.

To try the app's handling of a notification without APNs, the simulator takes a payload directly:

```sh
echo '{"aps":{"alert":{"title":"bob","body":"hello"}},"conversation_id":"1"}' > push.json
xcrun simctl push booted fi.misaki.wrinkle push.json
```

## Things to know

- Only the TLS edge (8443) and the web client's server (8081) face the network. The backends, the balancer and the cluster serve devices on loopback port 8080, and side-by-side gateways on loopback 8180 to 8183. The infrastructure's ports (MySQL through vtgate without a password, Kafka, Kafka Connect, Valkey, Prometheus, Grafana) are published on loopback, and the internal RPC between gateway and services listens on loopback too.
- Accounts have passwords, stored as argon2id hashes, and sign-in gives each device a token. There is no password recovery: a forgotten password means a new account, or `make infra-reset` for everyone.
- `http://localhost:8081/monitor` on the host, with `make web` running, links to every monitoring page (Grafana, Prometheus, the balancers' statistics, Kafka Connect, Vitess, each native backend's metrics and profiles) and shows which answer. It is served only to the host itself, since those pages listen on loopback; from another device it is a 404.
- Grafana shows the backend's connections, requests and fan-out while you test. To open it from another device, start the stack with `GRAFANA_BIND=0.0.0.0 make infra-up` and browse to `http://<address>:3000`; it has no login.
