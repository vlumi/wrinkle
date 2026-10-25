# Web client

The web client is a React app over the contract, built with Vite to static files and served by a small Node server ([server.mjs](server.mjs)) with no dependencies. It speaks the WebSocket contract to any gateway, with the contract's generated code from protobuf-es (the same as the TypeScript backend's), zustand for the store the screens read, and WebCrypto for the end-to-end encryption. It needs Node 22 or later. Using it on the local network (the certificate, the TLS edge, the gateway address) is in [docs/local-network.md](../../docs/local-network.md#the-web-client); the design common to every client and backend is in [docs/architecture.md](../../docs/architecture.md).

## Layout

One folder per feature under `src/`, each with its model (pure functions over the store's data, unit-tested), its actions (requests over the connection) and its views:

```text
src/main.tsx, App.tsx   the entry point and the screens' frame
src/session/            the device's session: sign-in and Connect, the link (connection, keys, cursor), sync and pushed events, what the browser remembers
src/connection/         one WebSocket connection: frames, requests matched to responses, reconnecting; the status badge
src/signin/             the sign-in screen and the gateways signed in through
src/account/            settings: name, icon or picture, password, sign-out, account deletion
src/devices/            the account's devices: rename, sign out another
src/conversations/      the conversation list, a conversation, the composer, messages sent but not yet confirmed
src/groups/             new groups, members, topic and admins
src/notes/              the user's notes to themselves
src/contacts/           contacts, requests and blocks, the banner in a conversation with someone not a contact
src/reactions/          reactions to messages
src/stickers/           stickers and the palette
src/attachments/        pictures and files: encrypted, uploaded, viewed and downloaded
src/blobs/              uploads to slots from CreateUpload, and blob addresses
src/e2ee/               the cryptography, the key store, opening sealed content, key epochs, sharing history with a new device, safety numbers and verification
src/gen/                the generated contract (make proto)
src/ids.ts, storage.ts  ids as hex, and localStorage under wrinkle.*
server.mjs              serves dist/ over HTTPS with the certificate from make certs, or over HTTP on loopback only
monitor.mjs             the /monitor page, links to every dashboard, for requests from this host
test/                   the browser test and its runner
```

## Building, running, testing

| Target           | What                                                                               |
| ---------------- | ---------------------------------------------------------------------------------- |
| `make build-web` | `tsc` and `vite build`, into `dist/`                                               |
| `make web`       | builds and serves the client on :8081 to the network over HTTPS, with the TLS edge |
| `make web-dev`   | the same with live reload, for working on the client                               |
| `make web-lint`  | `tsc`, eslint and prettier                                                         |
| `make web-unit`  | the unit tests, vitest under jsdom (`src/**/*.test.ts`)                            |
| `make web-test`  | the browser test against the Go monolith                                           |

`make web-lint` and `make web-unit` are part of `make lint` and `make test`, and `make web-test` of `make ci`. [test/run.sh](test/run.sh) builds the client, starts the backend it is given and serves the build on loopback; [browser_test.mjs](test/browser_test.mjs) then drives it in headless Chrome over the DevTools protocol, with no dependencies beyond Node and Chrome: two users exchanging messages, read receipts, safety numbers, deletion, notes, profile changes, a second device that gets the history shared and is signed out, account deletion, contacts and blocks, a group with its members, topic and key epochs, reactions, stickers and attachments.

## Sync

The browser keeps its credentials and its cursor into the user's log in localStorage ([saved.ts](src/session/saved.ts)); the conversations and messages live in memory and are fetched again on each load. After Connect ([connect.ts](src/session/connect.ts)), a device without a cursor (new, or one that started over) takes Connect's log head, so its past comes from the conversation list and history requests rather than the log. It then lists the conversations and syncs from its cursor ([sync.ts](src/session/sync.ts)), 200 events a request until `has_more` is false, and acks the cursor. Pushed events in between apply when they follow the cursor; one already applied is ignored, and a gap syncs again. When Sync fails with `ERROR_CODE_CURSOR_EXPIRED`, the log no longer reaches back to the cursor, and `startOver` sets the cursor to 0, empties the data held and restarts the connection, so the next Connect starts from the head and lists everything again.

## End-to-end encryption

Messages, reactions, stickers and attachments are sealed with the conversation's epoch keys, as [docs/architecture.md](../../docs/architecture.md#end-to-end-encryption) describes, in [crypto.ts](src/e2ee/crypto.ts) against the test vectors every client shares. The device's key pair and the epoch keys are in IndexedDB, one database per signed-in device, deleted when it signs out ([store.ts](src/e2ee/store.ts)); the private key is a non-extractable `CryptoKey`, which a script on the page can use but not read, while the epoch keys stay extractable, since they are wrapped for the user's new devices. The keys are safe from the backend and the network, not from scripts running on the page's origin. Verified contacts' fingerprints are remembered in the browser, per user.
