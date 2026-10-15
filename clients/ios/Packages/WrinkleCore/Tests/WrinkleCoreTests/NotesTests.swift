import SwiftData
import XCTest

@testable import WrinkleCore

/// The user's notes in the store: told apart from direct conversations and groups, found by
/// kind, and the user's own messages there never unread.
@MainActor
final class NotesTests: XCTestCase {
    private let notesID = Data(repeating: 8, count: 16)
    private let conv7 = Data(repeating: 7, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)

    private func notes() -> Wrinkle_V1_Conversation {
        .with {
            $0.conversationID = notesID
            $0.kind = .notes
            $0.memberCount = 1
            $0.member = true
        }
    }

    func testNotesAreTheirOwnKind() throws {
        let store = try Store.inMemory()
        XCTAssertNil(store.notesConversation())
        _ = store.upsert(
            .with {
                $0.conversationID = conv7
                $0.kind = .direct
                $0.peer.userID = bob
                $0.peer.name = "bob"
            })
        XCTAssertNil(store.notesConversation())

        let stored = store.upsert(notes())
        XCTAssertTrue(stored.isNotes)
        XCTAssertFalse(stored.isGroup)
        XCTAssertFalse(stored.isDirect)
        XCTAssertFalse(stored.isFormer)
        XCTAssertEqual(stored.title, "Notes")
        XCTAssertEqual(stored.peerID, "")
        XCTAssertEqual(store.notesConversation()?.id, notesID.hex)
        XCTAssertEqual(store.conversation(conv7.hex)?.isDirect, true)
        // Listed again: still the notes.
        _ = store.upsert(notes())
        XCTAssertTrue(stored.isNotes)
        XCTAssertEqual(try store.context.fetchCount(FetchDescriptor<StoredConversation>()), 2)
    }

    func testOwnNotesAreRead() throws {
        let store = try Store.inMemory(keys: TestSealing.keys(notesID))
        let stored = store.upsert(notes())
        _ = store.apply(.message(1, in: notesID, from: me, "remember the milk"), me: me.hex)
        XCTAssertEqual(stored.lastText, "remember the milk")
        XCTAssertEqual(stored.unread, 0)
    }

    func testNotesNameNoOneForTheExtension() throws {
        let store = try Store.inMemory()
        _ = store.upsert(notes())
        XCTAssertEqual(store.sharedNames(), SharedNames())
    }
}
