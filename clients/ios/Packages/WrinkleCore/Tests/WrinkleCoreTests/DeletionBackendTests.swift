import XCTest

@testable import WrinkleCore

/// Deleting a message against the live backend: the peer hears of it, history keeps a
/// tombstone, and only the sender may delete.
extension BackendTests {
    func testDeleteMessageRoundTrip() async throws {
        let (alice, _) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        let conv = try await alice.openConversation(peerName: b.user.name)
        let id = conv.conversationID
        let sent = try await alice.sendMessage(
            conversationID: id, clientMessageID: "m1", text: "oops")
        _ = try await next(bob.events) { $0.message.message.seq == sent.seq }

        let denied = await code { try await bob.deleteMessage(conversationID: id, seq: sent.seq) }
        XCTAssertEqual(denied, .permissionDenied)
        let unknown = await code { try await alice.deleteMessage(conversationID: id, seq: 99) }
        XCTAssertEqual(unknown, .notFound)

        try await alice.deleteMessage(conversationID: id, seq: sent.seq)
        let event = try await next(bob.events) { $0.messageDeleted.seq == sent.seq }
        XCTAssertEqual(event.messageDeleted.conversationID, id)
        XCTAssertTrue(event.messageDeleted.hasDeletedAt)
        // Deleting again is no error.
        try await alice.deleteMessage(conversationID: id, seq: sent.seq)

        let history = try await bob.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        XCTAssertEqual(history.map(\.seq), [sent.seq])
        XCTAssertTrue(history.first?.hasDeletedAt == true)
        XCTAssertEqual(history.first?.text, "")
        XCTAssertNil(history.first?.content)
        await alice.close()
        await bob.close()
    }
}
