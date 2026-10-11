import XCTest

@testable import WrinkleCore

/// Reactions against the live backend: set, replaced and removed, each reaching the peer, who
/// opens them; history returns the latest set.
extension BackendTests {
    private static func reacted(_ e: Wrinkle_V1_Event) -> Bool {
        if case .reaction = e.body { return true }
        return false
    }

    func testReactionRoundTrip() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        let conv = try await alice.openConversation(peerName: b.user.name)
        let id = conv.conversationID
        let sent = try await send(
            alice, "react to this", to: id, members: .direct(peer: b.user.userID), id: "r1")
        _ = try await next(bob.events) { $0.message.message.seq == sent.seq }

        let bobKeys = try keys(bob)
        let aliceKeys = try keys(alice)
        let members = KeyManager.Members.direct(peer: a.user.userID)
        let opened = { (s: Wrinkle_V1_Sealed) in
            aliceKeys.open(s, reactionsOf: b.user.userID, to: sent.seq, in: id)
        }

        try await bobKeys.react(["👍"], to: sent.seq, in: id, members: members, bob)
        let set = try await next(alice.events) { Self.reacted($0) }
        XCTAssertEqual(set.reaction.conversationID, id)
        XCTAssertEqual(set.reaction.seq, sent.seq)
        XCTAssertEqual(set.reaction.userID, b.user.userID)
        XCTAssertFalse(set.reaction.sealed.ciphertext.contains(Data("👍".utf8)))
        XCTAssertEqual(opened(set.reaction.sealed), ["👍"])

        try await bobKeys.react(["❤️", "😂"], to: sent.seq, in: id, members: members, bob)
        let replaced = try await next(alice.events) { Self.reacted($0) }
        XCTAssertEqual(opened(replaced.reaction.sealed), ["❤️", "😂"])

        let history = try await alice.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        let reactions = try XCTUnwrap(history.first { $0.seq == sent.seq }).reactions
        XCTAssertEqual(reactions.map(\.userID), [b.user.userID])
        XCTAssertEqual(reactions.first.flatMap { opened($0.sealed) }, ["❤️", "😂"])

        try await bobKeys.react([], to: sent.seq, in: id, members: members, bob)
        let removed = try await next(alice.events) { Self.reacted($0) }
        XCTAssertEqual(removed.reaction.userID, b.user.userID)
        XCTAssertFalse(removed.reaction.hasSealed)
        let after = try await alice.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        XCTAssertTrue(try XCTUnwrap(after.first { $0.seq == sent.seq }).reactions.isEmpty)

        let notFound = await code {
            try await bobKeys.react(["👍"], to: 99, in: id, members: members, bob)
        }
        XCTAssertEqual(notFound, .notFound)
        await alice.close()
        await bob.close()
    }
}
