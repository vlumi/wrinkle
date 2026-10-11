import SwiftData
import XCTest

@testable import WrinkleCore

@MainActor
final class StoreTests: XCTestCase {
    private let conv7 = Data(repeating: 7, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)

    private func makeStore() throws -> Store {
        let container = try ModelContainer(
            for: Store.schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        return Store(context: ModelContext(container), keys: TestSealing.keys(conv7))
    }

    func testMessageEventUpdatesConversation() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        conv.peer.userID = bob
        conv.peer.name = "bob"
        _ = store.upsert(conv)

        var event = Wrinkle_V1_Event()
        event.cursor = 1
        event.message.message = .with {
            $0.conversationID = conv7
            $0.seq = 1
            $0.senderUserID = bob
            $0.clientMessageID = "m1"
            $0.sentAt = .init(date: Date())
            $0.seal("hello")
        }
        let touched = store.apply(event, me: me.hex)
        XCTAssertEqual(touched?.lastSeq, 1)
        XCTAssertEqual(touched?.lastText, "hello")
        XCTAssertEqual(touched?.unread, 1)
        // Applying the same event again changes nothing.
        _ = store.apply(event, me: me.hex)
        XCTAssertEqual(try store.context.fetchCount(FetchDescriptor<StoredMessage>()), 1)
    }

    func testOwnMessageIsRead() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        conv.peer.userID = bob
        let stored = store.upsert(conv)

        var event = Wrinkle_V1_Event()
        event.message.message = .with {
            $0.conversationID = conv7
            $0.seq = 1
            $0.senderUserID = me
            $0.clientMessageID = "m1"
            $0.seal("sent from another device")
        }
        _ = store.apply(event, me: me.hex)
        XCTAssertEqual(stored.readSeq, 1)
        XCTAssertEqual(stored.unread, 0)
    }

    func testReadEventsTellReaderFromPeer() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        conv.lastSeq = 3
        conv.peer.userID = bob
        let stored = store.upsert(conv)

        var mine = Wrinkle_V1_Event()
        mine.read = .with {
            $0.conversationID = conv7
            $0.userID = me
            $0.seq = 2
        }
        _ = store.apply(mine, me: me.hex)
        XCTAssertEqual(stored.readSeq, 2)
        XCTAssertEqual(stored.peerReadSeq, 0)
        XCTAssertEqual(stored.unread, 1)

        var theirs = Wrinkle_V1_Event()
        theirs.read = .with {
            $0.conversationID = conv7
            $0.userID = bob
            $0.seq = 3
        }
        _ = store.apply(theirs, me: me.hex)
        XCTAssertEqual(stored.peerReadSeq, 3)
        // Backwards is ignored.
        _ = store.apply(mine, me: me.hex)
        XCTAssertEqual(stored.readSeq, 2)
    }

    func testOnlyANewerMessageBecomesTheLatest() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        let stored = store.upsert(conv)
        let message = { [conv7 = self.conv7] (seq: UInt64, text: String) in
            Wrinkle_V1_Message.with {
                $0.conversationID = conv7
                $0.seq = seq
                $0.seal(text)
            }
        }
        XCTAssertTrue(stored.noteLatest(message(2, "second")))
        XCTAssertFalse(stored.noteLatest(message(1, "first")))
        XCTAssertEqual(stored.lastSeq, 2)
        // Sealed until the store opens it.
        XCTAssertNotNil(stored.lastSealed)
        store.open(stored)
        XCTAssertNil(stored.lastSealed)
        XCTAssertEqual(stored.lastText, "second")
    }

    func testProfileEventRenamesPeerEverywhere() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        conv.peer.userID = bob
        conv.peer.name = "bob"
        let stored = store.upsert(conv)

        var event = Wrinkle_V1_Event()
        event.profile.user = .with {
            $0.userID = bob
            $0.name = "Robert"
            $0.icon = "owl"
        }
        XCTAssertNil(store.apply(event, me: me.hex))
        XCTAssertEqual(stored.peerName, "Robert")
        XCTAssertEqual(stored.peerIcon, "owl")

        // The user's own profile is not a peer's.
        event.profile.user.userID = me
        event.profile.user.name = "me"
        _ = store.apply(event, me: me.hex)
        XCTAssertEqual(stored.peerName, "Robert")
    }

    func testResetForgetsEverything() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        _ = store.upsert(conv)
        store.insert(
            .with {
                $0.conversationID = conv7
                $0.seq = 1
            })
        store.save()
        store.reset()
        XCTAssertEqual(try store.context.fetchCount(FetchDescriptor<StoredConversation>()), 0)
        XCTAssertEqual(try store.context.fetchCount(FetchDescriptor<StoredMessage>()), 0)
    }

    func testOnlyOlderStoresAreDiscarded() {
        let current = "wrinkle-v2.store"
        XCTAssertTrue(Store.isOldStore("default.store", current: current))
        XCTAssertTrue(Store.isOldStore("default.store-wal", current: current))
        XCTAssertTrue(Store.isOldStore("wrinkle-v1.store-shm", current: current))
        XCTAssertFalse(Store.isOldStore("wrinkle-v2.store", current: current))
        XCTAssertFalse(Store.isOldStore("wrinkle-v2.store-wal", current: current))
        XCTAssertFalse(Store.isOldStore("other.sqlite", current: current))
    }
}

extension StoreTests {
    func testPeerAvatarAndDeletedAccount() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = Data(repeating: 8, count: 16)
        conv.peer.userID = Data(repeating: 3, count: 16)
        conv.peer.name = "carol"
        conv.peer.avatar = Data(repeating: 0xcd, count: 16)
        let stored = store.upsert(conv)
        XCTAssertEqual(stored.peerAvatar, String(repeating: "cd", count: 16))
        XCTAssertEqual(stored.peerDisplayName, "carol")

        var event = Wrinkle_V1_Event()
        event.profile.user = .with {
            $0.userID = Data(repeating: 3, count: 16)
            $0.deleted = true
        }
        _ = store.apply(event, me: Data(repeating: 1, count: 16).hex)
        XCTAssertTrue(stored.peerDeleted)
        XCTAssertEqual(stored.peerAvatar, "")
        XCTAssertEqual(stored.peerName, "")
        XCTAssertEqual(stored.peerDisplayName, "Deleted account")
    }
}

/// Sealed content in the store: locked until a key opens it, in the list and the history.
@MainActor
final class SealedStoreTests: XCTestCase {
    private let conv7 = Data(repeating: 7, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)

    func testLockedUntilTheKeyArrives() throws {
        let container = try ModelContainer(
            for: Store.schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        let keys = EpochKeys(vault: MemoryVault())
        let store = Store(context: ModelContext(container), keys: keys)
        _ = store.upsert(
            .with {
                $0.conversationID = conv7
                $0.peer.userID = bob
            })
        var event = Wrinkle_V1_Event()
        event.message.message = .with {
            $0.conversationID = conv7
            $0.seq = 1
            $0.senderUserID = bob
            $0.clientMessageID = "m1"
            $0.seal("later", epoch: 3)
        }
        let c = try XCTUnwrap(store.apply(event, me: me.hex))
        let m = try XCTUnwrap(store.message(1, in: conv7.hex))
        XCTAssertTrue(m.isLocked)
        XCTAssertEqual(m.text, "")
        XCTAssertNotNil(c.lastSealed)
        XCTAssertEqual(store.locked(), [conv7.hex: [3]])

        keys.put(conv7, epoch: 3, key: TestSealing.key)
        store.reopen(conv7.hex)
        XCTAssertFalse(m.isLocked)
        XCTAssertEqual(m.text, "later")
        XCTAssertEqual(c.lastText, "later")
        XCTAssertNil(c.lastSealed)
        XCTAssertTrue(store.locked().isEmpty)
    }

    func testWrongKeyStaysLocked() throws {
        let container = try ModelContainer(
            for: Store.schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        let keys = EpochKeys(vault: MemoryVault())
        keys.put(conv7, epoch: 1, key: Data(repeating: 9, count: 32))
        let store = Store(context: ModelContext(container), keys: keys)
        var m = Wrinkle_V1_Message.with {
            $0.conversationID = conv7
            $0.seq = 1
            $0.senderUserID = bob
            $0.clientMessageID = "m1"
        }
        m.seal("secret")
        store.insert(m)
        XCTAssertEqual(store.message(1, in: conv7.hex)?.isLocked, true)
    }
}
