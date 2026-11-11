# Agent instructions

Wrinkle is a prototype messaging backend, one-to-one and in groups, implemented in several languages against one contract, with shared clients and a load generator for comparing how the implementations scale. README.md describes the design; ROADMAP.md has the plan and its open questions.

## Rules

- The project stands on its own: no references to where it came from, or to any company or product as its origin, in code, documentation, commit messages or anywhere else in the repository. Technical references to well-known systems are fine where they explain a choice.
- The Protobuf contract in `proto/` is the source of truth. Every backend implements all of it and passes the conformance tests; clients and the load generator must work against any backend unchanged. Contract changes update all implementations, or are recorded in ROADMAP.md as pending for those not yet updated.
- Infrastructure is shared: backends never get their own databases or brokers, so measurements compare the backends alone.
- Code is organized by feature, not by layer (no `controller/`, `service/`, `repository/` packages).
- SQL keywords and types in uppercase; table and column names in lowercase snake_case.
- Comments: minimal, English, en-US spelling.
- Measurement results always record the date, commit, machine, and configuration. Never compare numbers taken on different machines or infrastructure settings.
- Every change reaches `main` through a pull request that hosted CI has passed (`.github/workflows/ci.yml`); `main` takes no direct pushes. Hosted CI runs `make ci-linux` (formatting checks, linters and unit tests for every implementation, the generated code, then the conformance tests against each backend and the web client test on the stack in Docker) and `make ci-macos` (the iOS client's lint, build and the tests that need no backend), and skips both when only Markdown changed. `make ci` is all of it locally, on the local infrastructure, and the only place the iOS client is tested against a backend: run it before merging a change to the iOS client or the contract.
- Routine operations are make targets with a `##` description under a `##@` group, so plain `make` lists them; scripts do the work, the Makefile names it. A new backend variant, client or measurement gets its targets.
- Local, uncommitted settings (the Apple team, the APNs key) live in `.env` at the root, documented in `.env.example`; never commit secrets or keys.
- Frequent small commits and small pull requests; merge when CI passes.

## Toolchains

Latest stable or LTS of everything: Go, JDK 25 (Homebrew `openjdk@25`, found through `java_home`), Elixir on current OTP, Rust through rustup, Node 26 or later (the web client needs 22, the TypeScript backend 26), Xcode 26 with Swift 6. `make tools` installs the code generators and linters. Docker Desktop needs at least 8 GB of memory; 12 GB for the cluster, 14 GB with its Kubernetes enabled for `make k8s-up` (side-by-side backends run natively and need no more). Each backend has a Dockerfile for the cluster; neither `make ci` nor hosted CI builds them, so run `make ci-images` after changing a Dockerfile or a backend's build.
