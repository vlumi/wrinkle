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
        return Store(context: ModelContext(container))
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
            $0.text = "hello"
            $0.sentAt = .init(date: Date())
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
            $0.text = "sent from another device"
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
                $0.text = text
            }
        }
        XCTAssertTrue(stored.noteLatest(message(2, "second")))
        XCTAssertFalse(stored.noteLatest(message(1, "first")))
        XCTAssertEqual(stored.lastSeq, 2)
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
