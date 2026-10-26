# iOS client

The iOS client is a SwiftUI app for iOS 26 in Swift 6, with a notification service extension that decrypts message pushes. It speaks the WebSocket contract to any gateway through swift-protobuf, keeps what it has seen in SwiftData, and seals content with CryptoKit. The logic is a Swift package, so its tests run headless on the Mac with `swift test`; the Xcode project is generated from [project.yml](project.yml) by xcodegen and not committed. Running it in the simulator or on an iPhone, with push notifications, is in [docs/local-network.md](../../docs/local-network.md#the-ios-app); the design common to every client and backend is in [docs/architecture.md](../../docs/architecture.md).

## Layout

```text
project.yml                      the xcodegen spec: the app, the extension, the app group, signing from .env
Sources/iOS/                     the app shell: the entry point, Info.plist, entitlements
Sources/NotificationService/     the extension: words a message push with the epoch keys the app shares through the app group
Packages/WrinkleCore/
  Sources/WrinkleCore/           the contract types, the connection, the local store, the cryptography; no UI
    Contract/                    the generated contract (make proto)
    Transport/                   one WebSocket connection: frames, requests matched to responses
    Storage/                     the SwiftData store of conversations, messages, members and reactions
    E2EE/                        sealing and opening, device and epoch keys, the Keychain vaults
    Verification/                contacts verified by safety number
    Accounts/, Groups/, Contacts/, Notes/, Reactions/, Stickers/, Attachments/, Blobs/, Deletion/
                                 each feature's requests, rules and stored types
    Notifications/               the content of a push, for the extension
  Sources/WrinkleKit/            the session (AppState) and the SwiftUI screens, one folder per feature
  Tests/WrinkleCoreTests/        unit tests, and tests against a live backend
Scripts/                         generate, build, run and test, as the make targets call them
```

## Building, running, testing

| Target | What |
|---|---|
| `make ios-sim` | the app in an iPhone simulator (`DEVICE="17 Pro"` to pick one) |
| `make ios-device` | the app on a paired iPhone, signed with `WRINKLE_TEAM_ID` from `.env` (`DEVICE=<name>`) |
| `make ios-project` | regenerates the Xcode project, to open in Xcode |
| `make build-ios` | the app for the simulator, unsigned, so no team is needed |
| `make ios-lint` | SwiftLint and `swift format lint`, both strict |
| `make ios-test` | the package tests against the Go monolith |

`make ci` runs `ios-lint`, `build-ios` and `ios-test`. [Scripts/test.sh](Scripts/test.sh) runs the package tests on the Mac; those that need a backend expect one at `ws://localhost:8080/v1/ws` and skip themselves without it, while [Scripts/test-backend.sh](Scripts/test-backend.sh), which `make ios-test` uses, starts the backend it is given and requires them to run.

## Sync

The app keeps the device token in the Keychain and its cursor into the user's log in `UserDefaults`, per device ([SavedSession.swift](Packages/WrinkleCore/Sources/WrinkleKit/Session/SavedSession.swift)). After Connect ([AppState.swift](Packages/WrinkleCore/Sources/WrinkleKit/Session/AppState.swift)), a device without a cursor takes Connect's log head, so its past comes from the conversation list and history requests rather than the log. It then lists the conversations, a page at a time until `next_page_token` is empty, and syncs from its cursor ([AppState+Sync.swift](Packages/WrinkleCore/Sources/WrinkleKit/Session/AppState+Sync.swift)), up to 200 events a request until `has_more` is false, saving the store and moving the cursor after each page, and acks the cursor. A page may be shorter than asked, as the server ends pages before 512 KiB, so history too goes by `has_more` rather than the count, and the conversation's keys are fetched after the last epoch returned while `has_more` holds. Frames from the server may reach the contract's 1 MiB, so the socket takes up to 2 MiB. Pushed events in between apply when they follow the cursor; one already applied is ignored, and a gap syncs again. When Sync fails with `ERROR_CODE_CURSOR_EXPIRED`, `startOver` calls `Store.resetHistory`, which forgets the conversations, messages, members and reactions but keeps the safety-number verifications, and sets the cursor to 0. During Connect the app then goes on from Connect's log head and lists the conversations again; on a gap in pushed events it closes the connection, and the next Connect starts from the head.

## End-to-end encryption

Messages, reactions, stickers and attachments are sealed with the conversation's epoch keys, as [docs/architecture.md](../../docs/architecture.md#end-to-end-encryption) describes, in [E2EE.swift](Packages/WrinkleCore/Sources/WrinkleCore/E2EE/E2EE.swift) against the test vectors every client shares. The device's private key stays in the app's own Keychain; the epoch keys, and the names the extension words pushes with, go to the app group's Keychain, which the notification service extension shares ([Vaults.swift](Packages/WrinkleCore/Sources/WrinkleCore/E2EE/Vaults.swift)). The extension replaces the push's `New message` with the decrypted content when it holds the epoch key, and leaves it without one, without the sealed content (a payload that would have been too large) or for a sender the user blocked.
