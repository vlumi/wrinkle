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

   The first run builds the arm64 Vitess image from source and takes a few minutes; later runs start in about 20 seconds. `make infra-down` stops everything and discards the data.

4. Keep the Mac awake: System Settings, Energy, turn on "Prevent automatic sleeping when the display is off".

5. Find the address devices will use, the Bonjour name or the LAN IP:

   ```sh
   echo "$(scutil --get LocalHostName).local"
   ipconfig getifaddr en0
   ```

   Devices connect to `ws://<address>:8080/v1/ws`.

The generated code is committed, so the code generators (`make tools`) are not needed to run anything.

## Backends

Run one backend at a time: they all serve port 8080. Stop one with Ctrl-C before starting the next; they share the database, so users and conversations carry over.

The first time each program listens, macOS asks whether to accept incoming connections; allow it. If the prompt was missed, add the program under System Settings, Network, Firewall, Options. Each backend is a different program to the firewall: `bin/wrinkle`, `java`, `beam.smp` and the Rust binary. Build binaries to a fixed path rather than using `go run`, which builds to a new temporary path every time and so asks again.

### Go

```sh
go build -o bin/wrinkle ./backends/go/cmd/wrinkle
bin/wrinkle
```

The same backend as a gateway and a services process, the shape the Elixir and Rust gateways use:

```sh
backends/go/split.sh
```

### Java

```sh
brew install openjdk@25
mkdir -p ~/Library/Java/JavaVirtualMachines
ln -sfn /opt/homebrew/opt/openjdk@25/libexec/openjdk.jdk ~/Library/Java/JavaVirtualMachines/openjdk-25.jdk
export JAVA_HOME=$(/usr/libexec/java_home -v 25)

make java-build
backends/java/build/install/wrinkle/bin/wrinkle
```

The symlink lets `java_home` find the Homebrew JDK, which the Makefile uses. The first build downloads Gradle and the dependencies.

### Elixir

The Elixir implementation is a gateway; its run script starts the Go services and fan-out beside it, so the Go binary has to be built first.

```sh
brew install elixir
mix local.hex --force
mix local.rebar --force

go build -o bin/wrinkle ./backends/go/cmd/wrinkle
make elixir-deps
backends/elixir/run.sh
```

### Rust

Also a gateway, run beside the Go services.

```sh
brew install rustup
export PATH="/opt/homebrew/opt/rustup/bin:$PATH"
rustup default stable

go build -o bin/wrinkle ./backends/go/cmd/wrinkle
make rust-build
backends/rust/run.sh
```

## Clients

### Web client

`make web` serves only to the Mac itself. To reach it from other devices, serve the repository root on all interfaces:

```sh
python3 -m http.server 8081 --directory . --bind 0.0.0.0
```

Open `http://<address>:8081/clients/web/` on the device. The gateway field defaults to the same host on port 8080; a browser that signed in before remembers the address it used. The page loads protobuf.js from a CDN, so devices need internet access as well as the local network. Each browser is a separate device; a private window is a quick second one.

### iOS client in the simulator

On the host itself, with Xcode 26:

```sh
brew install xcodegen
clients/ios/Scripts/run-ios.sh
```

The simulator shares the host's network, so the default `ws://localhost:8080/v1/ws` works.

### iOS client on an iPhone

Needs an Apple developer team. Copy `.env.example` to `.env` at the repository root and set `WRINKLE_TEAM_ID`; the project generator signs with it. Turn on Developer Mode on the phone (Settings, Privacy & Security), connect it by cable once and trust the Mac, then:

```sh
brew install xcodegen
clients/ios/Scripts/run-device.sh
```

The script builds, installs and launches the app; with several phones paired, pass part of the phone's name. The first build registers the app id and a development profile through Xcode's automatic signing, so the Mac's Xcode must be signed in to the team's account (Xcode, Settings, Accounts). On the phone, the app asks to find devices on the local network and to send notifications; allow both, then enter the host's address on the sign-in screen.

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

- Only the gateway (8080) and the page server (8081) face the network. The infrastructure's ports (MySQL through vtgate without a password, Kafka, Kafka Connect, Valkey, Prometheus, Grafana) are published on loopback, and the internal RPC between gateway and services listens on loopback too.
- Sign-in is by name only; anyone who can reach the host can connect as any user.
- Grafana shows the backend's connections, requests and fan-out while you test. To open it from another device, start the stack with `GRAFANA_BIND=0.0.0.0 make infra-up` and browse to `http://<address>:3000`; it has no login.
