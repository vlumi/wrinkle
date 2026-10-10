# Agent instructions

Wrinkle is a prototype one-to-one messaging backend, implemented in several languages against one contract, with shared clients and a load generator for comparing how the implementations scale. README.md describes the design; ROADMAP.md has the plan and its open questions.

## Rules

- Never mention where this project came from, any company or product it might be compared to as its origin, or any hiring process, in code, documentation, commit messages or anywhere else in the repository. Technical references to well-known systems are fine where they explain a choice.
- The Protobuf contract in `proto/` is the source of truth. Every backend implements all of it and passes the conformance tests; clients and the load generator must work against any backend unchanged. Contract changes update all implementations, or are recorded in ROADMAP.md as pending for those not yet updated.
- Infrastructure is shared: backends never get their own databases or brokers, so measurements compare the backends alone.
- Code is organized by feature, not by layer (no `controller/`, `service/`, `repository/` packages).
- SQL keywords and types in uppercase; table and column names in lowercase snake_case.
- Comments: minimal, English, en-US spelling.
- Measurement results always record the date, commit, machine, and configuration. Never compare numbers taken on different machines or infrastructure settings.
- Commit and push to `main` directly when something works; frequent small commits, no pull requests.

## Toolchains

Go (current stable), Java 25, Swift 6 with Xcode, Elixir and Rust (current stable) when their phases start. Docker Desktop needs at least 8 GB of memory; 12 GB to run two backends side by side.
