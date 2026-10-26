import XCTest

@testable import WrinkleCore

/// Pages against the live backend: conversations, history and keys come a page at a time.
extension BackendTests {
    func failure(_ work: () async throws -> some Any) async -> BackendError? {
        do {
            _ = try await work()
            return nil
        } catch {
            return error as? BackendError
        }
    }

    /// The conversation's two messages, one a page; a page says whether older ones follow.
    func historyPaged(_ c: Connection, _ id: Data) async throws {
        let newest = try await c.getMessagesPage(
            conversationID: id, beforeSeq: 0, limit: 1)
        XCTAssertEqual(newest.messages.map(\.seq), [2])
        XCTAssertTrue(newest.hasMore_p)
        let oldest = try await c.getMessagesPage(
            conversationID: id, beforeSeq: 2, limit: 1)
        XCTAssertEqual(oldest.messages.map(\.seq), [1])
        XCTAssertFalse(oldest.hasMore_p)
    }

    func testConversationsListedPageByPage() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        var ids: [Data] = []
        for peer in ["ios-bob", "ios-carol", "ios-dave"] {
            let (c, _) = try await registered(unique(peer))
            let conv = try await c.openConversation(peerName: a.user.name)
            try await send(
                c, "hello", to: conv.conversationID, members: .direct(peer: a.user.userID),
                id: "p1")
            ids.insert(conv.conversationID, at: 0)
            _ = try await next(alice.events) {
                $0.message.message.conversationID == conv.conversationID
            }
            await c.close()
        }
        // One a page, the most recently active first, as in a single page.
        let paged = try await alice.listConversations(pageSize: 1)
        XCTAssertEqual(paged.map(\.conversationID), ids)
        let whole = try await alice.listConversations()
        XCTAssertEqual(whole.map(\.conversationID), ids)
        await alice.close()
    }
}
