# Running on a local network

Run the stack and one backend on a Mac, then connect to it from phones and other computers on the same network for manual testing. The steps assume a fresh Apple-silicon Mac with Homebrew.

## The host

1. Install Docker Desktop and give it at least 8 GB of memory (Settings, Resources). Install the Go toolchain and git:

   ```sh
   brew install go git
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

   Devices connect to `ws://<address>:8080/v1/ws`.

The generated code is committed, so the code generators (`make tools`) are not needed to run anything.

## Backends

`make run-go-mono` and `make run-java-mono` each run a whole backend on port 8080, so one at a time; Ctrl-C stops one. The side-by-side setup, described below, instead runs services instances and gateways as separate processes. All of them share the database, so users and conversations carry over. `make` lists every target.

The first time each program listens, macOS asks whether to accept incoming connections; allow it. If the prompt was missed, add the program under System Settings, Network, Firewall, Options. Each backend is a different program to the firewall: `bin/wrinkle`, `java`, `beam.smp` and the Rust binary. The make targets build to fixed paths, so each is asked about once; `go run` would build to a new temporary path every time and ask again.

### Go

```sh
make run-go-mono
```

The Go gateway can also run as a separate process beside the Go services, as the Elixir and Rust gateways do; see [Side by side](#side-by-side).

### Java

```sh
brew install openjdk@25
mkdir -p ~/Library/Java/JavaVirtualMachines
ln -sfn /opt/homebrew/opt/openjdk@25/libexec/openjdk.jdk ~/Library/Java/JavaVirtualMachines/openjdk-25.jdk

make run-java-mono
```

The symlink lets `java_home` find the Homebrew JDK, which the Makefile uses. The first build downloads Gradle and the dependencies.

### Elixir

The Elixir implementation is a gateway, run beside the Go services as described in [Side by side](#side-by-side). Its toolchain:

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
make run-go-gw                     # gateways, each in its own terminal: ws://<address>:8180/v1/ws
make run-elixir-gw                 # :8181
make run-rust-gw                   # :8182
make run-java-gw                   # :8183
make lb-down                       # when done
```

- **Devices to gateways:** `ws://<address>:8080/v1/ws`, where the clients connect by default, goes to the healthy gateway with the fewest connections. Stop a gateway and the balancer marks it down within about two seconds; its devices reconnect and land on the others. To reach one gateway in particular, put its port in the client's gateway field.
- **Gateways to services:** each gateway keeps one HTTP/2 connection to the services balancer on 127.0.0.1:9100, which hands out each request separately, in turn, to the running instances. Instance *n* serves the RPC on 9100+*n*, on loopback only. Stop an instance and the rest take its requests and its share of the fan-out.

`http://localhost:8404/` on the host shows both balancers: each gateway and instance, its state, and what it has handled. A gateway refuses to start until the balancers and at least one services instance are up. `PORT=...` moves a gateway elsewhere, outside the balancer. The balancer holds port 8080, so stop it before `run-go-mono` or `run-java-mono`. Prometheus scrapes only port 8080, so these processes don't show in Grafana; the monitoring page links each one's metrics.

Java services instances store push tokens but send no pushes yet; with only Java instances running, iOS devices get no notifications.

### Cluster

The side-by-side setup in containers, from one command: the balancer, the services instances and the gateways, each in any number, on the infrastructure's network. The first run builds an image per backend, a few minutes; later runs take seconds.

```sh
make cluster-up                                  # 2 Go and 1 Java services, one gateway of each language
make cluster-up GO_GW=3 RUST_GW=0 JAVA_SERVICES=0  # rescales in place; counts not given stay as they are
make cluster-status                              # every container
make cluster-logs SERVICE=go-gw                  # follow one kind, or all without SERVICE
make cluster-down                                # remove the cluster; the infrastructure keeps running
```

The counts are `GO_SERVICES`, `JAVA_SERVICES`, `GO_GW`, `ELIXIR_GW`, `RUST_GW` and `JAVA_GW`, 0 to 8 each. A count not given keeps what is running, so after a code change a plain `make cluster-up` rebuilds the images and replaces the containers whose image changed, at the same counts. The defaults apply on a fresh start, after `cluster-down`. Devices connect to `ws://<address>:8080/v1/ws` as before, and `http://localhost:8404/` shows every replica the balancer has found. The balancer finds them by name through Docker's DNS: a kind scaled up joins within a few seconds, one scaled down drops out. Push settings come from `.env` as for the other targets.

The cluster is for trying the scaled shape, not for measuring: the containers run in Docker Desktop's virtual machine, behind its networking, so their numbers are not comparable with the native processes the measurements use. The cluster holds port 8080, like the native balancer and the monoliths, so one of them at a time. Its processes don't show in Grafana.

## Clients

### Web client

A React app built to static files and served by a small Node server. It needs Node 22 or later (`brew install node`):

```sh
make web
```

That installs the dependencies, builds the app, and serves it on port 8081 to the network. Open `http://<address>:8081/` on the device. The gateway field defaults to the same host on port 8080. Every gateway the browser has signed in through is listed under the field, most recent first: tap one to fill the field, or × to forget it. On a phone it shows one pane at a time, the conversations and then a conversation with a back button; on a wider screen both side by side. Each browser is a separate device; a private window is a quick second one.

For working on the client itself, `make web-dev` serves it with live reload on the same port.

### iOS client in the simulator

On the host itself, with Xcode 26:

```sh
brew install xcodegen
make ios-sim
```

The simulator shares the host's network, so the default `ws://localhost:8080/v1/ws` works.

### iOS client on an iPhone

Needs an Apple developer team. Copy `.env.example` to `.env` at the repository root and set `WRINKLE_TEAM_ID`; the project generator signs with it. Turn on Developer Mode on the phone (Settings, Privacy & Security), connect it by cable once and trust the Mac, then:

```sh
brew install xcodegen
make ios-device
```

That builds, installs and launches the app; with several phones paired, pick one with `DEVICE=<part of its name>`. The first build registers the app id and a development profile through Xcode's automatic signing, so the Mac's Xcode must be signed in to the team's account (Xcode, Settings, Accounts). On the phone, the app asks to find devices on the local network and to send notifications; allow both, then enter the host's address on the sign-in screen.

### Push notifications

The app registers for push and sends its token to the backend after every connect. While the app is in the background its connection is closed, and the Go backend's fan-out pushes new messages to it through APNs instead, with the unread count as badge. Tapping a notification opens the conversation.

Push needs an APNs key: in the developer account, Certificates, Identifiers & Profiles, Keys, create a key with "Apple Push Notifications service" enabled and download it (`AuthKey_<key id>.p8`). An App Store Connect API key is a different kind and does not work. Then in `.env`:

```sh
WRINKLE_TEAM_ID=ABCDE12345
WRINKLE_APNS_KEY=~/path/to/AuthKey_XXXXXXXXXX.p8
```

and restart the backend; it logs "push notifications on". Without a key everything else works and the app catches up when it is opened. Pushes are sent by the Go fan-out, so they work with the Go backend and with the Elixir and Rust gateways, which run beside it; the Java backend stores tokens but does not push yet.

To try the app's handling of a notification without APNs, the simulator takes a payload directly:

```sh
echo '{"aps":{"alert":{"title":"bob","body":"hello"}},"conversation_id":"1"}' > push.json
xcrun simctl push booted fi.misaki.wrinkle push.json
```

## Things to know

- Only the gateways (8080, or 8180 to 8183 side by side, with the balancer on 8080) and the web client's server (8081) face the network. The infrastructure's ports (MySQL through vtgate without a password, Kafka, Kafka Connect, Valkey, Prometheus, Grafana) are published on loopback, and the internal RPC between gateway and services listens on loopback too.
- Sign-in is by name only; anyone who can reach the host can connect as any user.
- `http://localhost:8081/monitor` on the host, with `make web` running, links to every monitoring page (Grafana, Prometheus, the load balancer's statistics, Kafka Connect, Vitess, each backend's metrics and profiles) and shows which answer. It is served only to the host itself, since those pages listen on loopback; from another device it is a 404.
- Grafana shows the backend's connections, requests and fan-out while you test. To open it from another device, start the stack with `GRAFANA_BIND=0.0.0.0 make infra-up` and browse to `http://<address>:3000`; it has no login.
