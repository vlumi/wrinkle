# Agent instructions

Wrinkle is a prototype messaging backend, one-to-one and in groups, implemented in several languages against one contract, with shared clients and a load generator for comparing how the implementations scale. README.md describes the design; ROADMAP.md has the plan and its open questions.

## Rules

- Never mention where this project came from, any company or product it might be compared to as its origin, or any hiring process, in code, documentation, commit messages or anywhere else in the repository. Technical references to well-known systems are fine where they explain a choice.
- The Protobuf contract in `proto/` is the source of truth. Every backend implements all of it and passes the conformance tests; clients and the load generator must work against any backend unchanged. Contract changes update all implementations, or are recorded in ROADMAP.md as pending for those not yet updated.
- Infrastructure is shared: backends never get their own databases or brokers, so measurements compare the backends alone.
- Code is organized by feature, not by layer (no `controller/`, `service/`, `repository/` packages).
- SQL keywords and types in uppercase; table and column names in lowercase snake_case.
- Comments: minimal, English, en-US spelling.
- Measurement results always record the date, commit, machine, and configuration. Never compare numbers taken on different machines or infrastructure settings.
- Run the full local CI, `make ci`, before every push to `main`, and only push when it passes; a push that changes only documentation (Markdown) needs none. It runs formatting checks, linters and unit tests for every implementation, then the conformance tests against each backend and the client tests on the local infrastructure. The repository is private, so hosted CI is limited; local CI is the gate.
- Routine operations are make targets with a `##` description under a `##@` group, so plain `make` lists them; scripts do the work, the Makefile names it. A new backend variant, client or measurement gets its targets.
- Local, uncommitted settings (the Apple team, the APNs key) live in `.env` at the root, documented in `.env.example`; never commit secrets or keys.
- Commit and push to `main` directly when something works; frequent small commits, no pull requests.

## Toolchains

Latest stable or LTS of everything: Go, JDK 25 (Homebrew `openjdk@25`, found through `java_home`), Elixir on current OTP, Rust through rustup, Node 26 or later (the web client needs 22, the TypeScript backend 26), Xcode 26 with Swift 6. `make tools` installs the code generators and linters. Docker Desktop needs at least 8 GB of memory; 12 GB for the cluster, 14 GB with its Kubernetes enabled for `make k8s-up` (side-by-side backends run natively and need no more). Each backend has a Dockerfile for the cluster; `make ci` does not build them, so run `make ci-images` after changing a Dockerfile or a backend's build.
