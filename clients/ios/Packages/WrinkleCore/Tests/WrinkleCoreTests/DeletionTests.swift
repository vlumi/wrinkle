import SwiftData
import XCTest

@testable import WrinkleCore

/// Messages deleted by their sender: the event, a synced tombstone, and history.
@MainActor
final class DeletionTests: XCTestCase {
    private let conv7 = Data(repeating: 7, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)
    private let deletedAt = Date(timeIntervalSince1970: 1_800_000_000)

    private func makeStore() throws -> (Store, StoredConversation) {
        let container = try ModelContainer(
            for: Store.schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        let store = Store(context: ModelContext(container), keys: TestSealing.keys(conv7))
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        conv.peer.userID = bob
        return (store, store.upsert(conv))
    }

    private func message(_ seq: UInt64, from sender: Data, _ text: String) -> Wrinkle_V1_Event {
        var event = Wrinkle_V1_Event()
        event.message.message = .with {
            $0.conversationID = conv7
            $0.seq = seq
            $0.senderUserID = sender
            $0.sentAt = .init(date: Date())
            $0.seal(text)
        }
        return event
    }

    private func deleted(_ seq: UInt64) -> Wrinkle_V1_Event {
        var event = Wrinkle_V1_Event()
        event.messageDeleted = .with {
            $0.conversationID = conv7
            $0.seq = seq
            $0.deletedAt = .init(date: deletedAt)
        }
        return event
    }

    func testDeletedEventTombstonesMessageAndPreview() throws {
        let (store, c) = try makeStore()
        _ = store.apply(message(1, from: bob, "first"), me: me.hex)
        _ = store.apply(message(2, from: bob, "second"), me: me.hex)

        XCTAssertIdentical(store.apply(deleted(1), me: me.hex), c)
        let first = try XCTUnwrap(store.message(1, in: conv7.hex))
        XCTAssertTrue(first.isDeleted)
        XCTAssertEqual(first.text, "")
        XCTAssertEqual(first.deletedAt, deletedAt)
        // Not the latest: the preview stays.
        XCTAssertFalse(c.lastDeleted)
        XCTAssertEqual(c.lastText, "second")

        _ = store.apply(deleted(2), me: me.hex)
        XCTAssertTrue(c.lastDeleted)
        XCTAssertEqual(c.lastText, "")
        XCTAssertEqual(c.lastSeq, 2)

        // A later message replaces the tombstone.
        _ = store.apply(message(3, from: bob, "third"), me: me.hex)
        XCTAssertFalse(c.lastDeleted)
        XCTAssertEqual(c.lastText, "third")
    }

    func testSyncedTombstoneAndHistory() throws {
        let (store, c) = try makeStore()
        var tombstone = message(1, from: bob, "")
        tombstone.message.message.deletedAt = .init(date: deletedAt)
        _ = store.apply(tombstone, me: me.hex)
        XCTAssertTrue(c.lastDeleted)
        XCTAssertEqual(store.message(1, in: conv7.hex)?.deletedAt, deletedAt)

        // History returning a deleted message already stored with its text.
        _ = store.apply(message(2, from: bob, "secret"), me: me.hex)
        var history = Wrinkle_V1_Message.with {
            $0.conversationID = conv7
            $0.seq = 2
            $0.senderUserID = bob
        }
        history.deletedAt = .init(date: deletedAt)
        XCTAssertFalse(store.insert(history))
        XCTAssertEqual(store.message(2, in: conv7.hex)?.text, "")
        XCTAssertEqual(store.message(2, in: conv7.hex)?.isDeleted, true)
    }

    func testOnlyOwnTextIsDeletable() throws {
        let (store, _) = try makeStore()
        _ = store.apply(message(1, from: me, "mine"), me: me.hex)
        _ = store.apply(message(2, from: bob, "theirs"), me: me.hex)
        var notice = message(3, from: me, "")
        notice.message.message.notice = .with { $0.topicChanged.topic = "T" }
        _ = store.apply(notice, me: me.hex)

        let mine = try XCTUnwrap(store.message(1, in: conv7.hex))
        XCTAssertTrue(mine.deletable(by: me.hex))
        XCTAssertFalse(try XCTUnwrap(store.message(2, in: conv7.hex)).deletable(by: me.hex))
        XCTAssertFalse(try XCTUnwrap(store.message(3, in: conv7.hex)).deletable(by: me.hex))
        store.markDeleted(1, in: conv7.hex, at: deletedAt)
        XCTAssertFalse(mine.deletable(by: me.hex))
        // Deleting again keeps the first time.
        store.markDeleted(1, in: conv7.hex, at: Date())
        XCTAssertEqual(mine.deletedAt, deletedAt)
    }
}
