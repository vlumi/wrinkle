import XCTest

@testable import WrinkleCore

/// The user's notes against the live backend: one per user, the same from every device, sealed
/// for the user's own devices only, and refused what only other kinds of conversation allow.
extension BackendTests {
    func testNotes() async throws {
        let name = unique("ios-alice")
        let (alice, a) = try await registered(name)
        let second = try await dial()
        let creds = try await second.signIn(name: name, password: Self.password, deviceName: "two")
        try await keyed(second, try await second.connect(deviceToken: creds.deviceToken))

        let notes = try await alice.openNotes()
        XCTAssertEqual(notes.kind, .notes)
        XCTAssertEqual(notes.memberCount, 1)
        XCTAssertTrue(notes.member)
        XCTAssertFalse(notes.admin)
        XCTAssertFalse(notes.hasPeer)
        XCTAssertEqual(notes.topic, "")
        let id = notes.conversationID
        let again = try await alice.openNotes()
        XCTAssertEqual(again.conversationID, id)
        let fromSecond = try await second.openNotes()
        XCTAssertEqual(fromSecond.conversationID, id)

        // Sealed for both of the user's devices.
        let sent = try await send(alice, "remember the milk", to: id, members: .notes, id: "n1")
        XCTAssertEqual(sent.sealed.epoch, 1)
        let got = try await next(second.events) {
            $0.message.message.conversationID == id && $0.message.message.seq == sent.seq
        }
        let opened = try await read(second, got.message.message)
        XCTAssertEqual(opened, "remember the milk")
        let listed = try await second.listConversations().filter { $0.kind == .notes }
        XCTAssertEqual(listed.map(\.conversationID), [id])
        XCTAssertEqual(listed.first?.lastSeq, sent.seq)

        let own = await code { try await alice.openConversation(peerName: a.user.name) }
        XCTAssertEqual(own, .invalidArgument)
        let members = await code { try await alice.getMembers(conversationID: id) }
        XCTAssertEqual(members, .failedPrecondition)
        await second.close()
        await alice.close()
    }
}
