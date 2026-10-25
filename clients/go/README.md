# Go client

`wrinkleclient` (module `github.com/vlumi/wrinkle/clients/go`) is a Go library for the WebSocket contract, used by the [conformance tests](../../conformance) and the [load generator](../../loadgen), which take it through a `replace` directive. One `Client` is one connection of one device. It has no UI and keeps nothing on disk: the caller holds the credentials, the cursor and the keys. The design common to every client and backend is in [docs/architecture.md](../../docs/architecture.md).

## Layout

```text
client.go        Dial, the read loop, Call (requests matched to responses by id), Send, Events and Pongs
requests.go      one method per request of the contract: accounts, conversations, messages, Sync, Ack, devices, keys, contacts
errors.go        Error, a backend's error response, and Code to read its code
keys.go          Keys, a device's end-to-end state: its key pair, the epoch keys it holds, other devices' keys; publishing and fetching epochs
send.go          SendText and SendContent, sealed with the current epoch; OpenMessage
reactions.go     sealed reactions, set and opened
stickers.go      stickers and the palette
attachments.go   attachments encrypted, uploaded, sent and downloaded
groups.go        groups: create, members, topic, admins
blobs.go         the blob address from a gateway URL, uploads to slots, downloads
e2ee/            the cryptography of content.proto, and testdata/vectors.json, the vectors every client checks
```

## Use

`Dial` opens a connection; the caller then registers or signs in and calls `Connect` with the device token. Pushed events arrive on `Events`, buffered up to `EventBuffer` (1024), after which the read loop waits; `Call` returns an error response as `*Error`. `EnableKeys` generates a key pair, publishes its public key and makes the connection use it, or `SetKeys` shares one `Keys` between connections of the same device; with keys set, `SendText` seals, publishing a new epoch first when needed, and retries once a send refused for its epoch, `OpenMessage` opens, and received events keep the keys current. The keys live only in memory.

`Sync` and `Ack` are plain requests: the client keeps no cursor and does not handle `ERROR_CODE_CURSOR_EXPIRED` itself. The caller decides, from `ConnectResponse`'s acked cursor and log head, where to sync from and what to do when the log no longer reaches back that far.

## Testing

`make go-lint` and `make go-test` cover this module with the others, both part of `make ci`. The unit tests check the shared vectors, the RFC 7748 and RFC 5869 vectors, tampering, blob encryption and safety numbers in `e2ee/`, and in [keys_test.go](keys_test.go), against scripted services, publishing the first epoch once, invalidation, group members, rotation retries, taking an epoch another device made and fetching an epoch key for opening. Everything against a backend is in the conformance tests.
