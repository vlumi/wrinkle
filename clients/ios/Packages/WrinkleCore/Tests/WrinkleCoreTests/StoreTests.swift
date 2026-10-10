import SwiftData
import XCTest

@testable import WrinkleCore

@MainActor
final class StoreTests: XCTestCase {
    private func makeStore() throws -> Store {
        let container = try ModelContainer(
            for: Store.schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        return Store(context: ModelContext(container))
    }

    func testMessageEventUpdatesConversation() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = 7
        conv.peer.userID = 2
        conv.peer.name = "bob"
        _ = store.upsert(conv)

        var event = Wrinkle_V1_Event()
        event.cursor = 1
        event.message.message = .with {
            $0.conversationID = 7
            $0.seq = 1
            $0.senderUserID = 2
            $0.clientMessageID = "m1"
            $0.text = "hello"
            $0.sentAt = .init(date: Date())
        }
        let touched = store.apply(event, me: 1)
        XCTAssertEqual(touched?.lastSeq, 1)
        XCTAssertEqual(touched?.lastText, "hello")
        XCTAssertEqual(touched?.unread, 1)
        // Applying the same event again changes nothing.
        _ = store.apply(event, me: 1)
        XCTAssertEqual(try store.context.fetchCount(FetchDescriptor<StoredMessage>()), 1)
    }

    func testReadEventsTellReaderFromPeer() throws {
        let store = try makeStore()
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = 7
        conv.lastSeq = 3
        conv.peer.userID = 2
        let stored = store.upsert(conv)

        var mine = Wrinkle_V1_Event()
        mine.read = .with {
            $0.conversationID = 7
            $0.userID = 1
            $0.seq = 2
        }
        _ = store.apply(mine, me: 1)
        XCTAssertEqual(stored.readSeq, 2)
        XCTAssertEqual(stored.peerReadSeq, 0)
        XCTAssertEqual(stored.unread, 1)

        var theirs = Wrinkle_V1_Event()
        theirs.read = .with {
            $0.conversationID = 7
            $0.userID = 2
            $0.seq = 3
        }
        _ = store.apply(theirs, me: 1)
        XCTAssertEqual(stored.peerReadSeq, 3)
        // Backwards is ignored.
        _ = store.apply(mine, me: 1)
        XCTAssertEqual(stored.readSeq, 2)
    }
}
