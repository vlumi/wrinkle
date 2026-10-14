import XCTest

@testable import WrinkleCore

/// Contacts against the live backend: a request and its acceptance, a block and what it
/// refuses, and an unblock.
extension BackendTests {
    /// Waits for the contact event telling the connection's user how another user stands.
    func contactEvent(
        _ c: Connection, about user: Data, _ state: Wrinkle_V1_ContactState
    ) async throws -> Wrinkle_V1_Contact {
        try await next(c.events) {
            $0.contact.contact.user.userID == user && $0.contact.contact.state == state
        }.contact.contact
    }

    /// Makes two users contacts, as groups require: each asks, and both hear of it.
    func befriend(
        _ a: Connection, _ aUser: Wrinkle_V1_User, _ b: Connection, _ bUser: Wrinkle_V1_User
    ) async throws {
        _ = try await a.addContact(userID: bUser.userID)
        _ = try await b.addContact(userID: aUser.userID)
        _ = try await contactEvent(a, about: bUser.userID, .contact)
        _ = try await contactEvent(b, about: aUser.userID, .contact)
    }

    private func states(_ c: Connection) async throws -> [Data: Wrinkle_V1_ContactState] {
        Dictionary(
            uniqueKeysWithValues: try await c.listContacts().map { ($0.user.userID, $0.state) })
    }

    func testContactsAndBlocking() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        let conv = try await alice.openConversation(peerName: b.user.name)
        let direct = KeyManager.Members.direct(peer: b.user.userID)
        _ = try await send(alice, "hello", to: conv.conversationID, members: direct, id: "m1")

        let me = await code { try await alice.addContact(name: a.user.name) }
        XCTAssertEqual(me, .invalidArgument)
        let nobody = await code { try await alice.addContact(name: self.unique("ios-nobody")) }
        XCTAssertEqual(nobody, .notFound)

        // Alice asks, by name ignoring case; bob accepts.
        let asked = try await alice.addContact(name: b.user.name.uppercased())
        XCTAssertEqual(asked.state, .outgoing)
        XCTAssertEqual(asked.user.userID, b.user.userID)
        let incoming = try await contactEvent(bob, about: a.user.userID, .incoming)
        XCTAssertEqual(incoming.user.name, a.user.name)
        _ = try await contactEvent(alice, about: b.user.userID, .outgoing)
        let again = try await alice.addContact(userID: b.user.userID)
        XCTAssertEqual(again.state, .outgoing)
        let accepted = try await bob.addContact(userID: a.user.userID)
        XCTAssertEqual(accepted.state, .contact)
        _ = try await contactEvent(alice, about: b.user.userID, .contact)
        _ = try await contactEvent(bob, about: a.user.userID, .contact)
        let alices = try await states(alice)
        XCTAssertEqual(alices, [b.user.userID: .contact])
        let bobs = try await states(bob)
        XCTAssertEqual(bobs, [a.user.userID: .contact])

        // Bob blocks alice: the contact ends, and alice never sees the block.
        try await bob.blockUser(userID: a.user.userID)
        _ = try await contactEvent(bob, about: a.user.userID, .blocked)
        _ = try await contactEvent(alice, about: b.user.userID, .unspecified)
        let blocking = try await states(bob)
        XCTAssertEqual(blocking, [a.user.userID: .blocked])
        let blocked = try await states(alice)
        XCTAssertEqual(blocked, [:])
        try await refusals(alice, a.user, bob, b.user, conv.conversationID)

        // Unblocking restores messaging, not the contact.
        try await bob.unblockUser(userID: a.user.userID)
        _ = try await contactEvent(bob, about: a.user.userID, .unspecified)
        let reopened = try await alice.openConversation(peerName: b.user.name)
        XCTAssertEqual(reopened.conversationID, conv.conversationID)
        _ = try await send(alice, "back", to: conv.conversationID, members: direct, id: "m3")
        let after = try await states(bob)
        XCTAssertEqual(after, [:])
        await alice.close()
        await bob.close()
    }

    /// What bob's block refuses alice, and what it asks of bob first.
    private func refusals(
        _ alice: Connection, _ a: Wrinkle_V1_User, _ bob: Connection, _ b: Wrinkle_V1_User,
        _ conversationID: Data
    ) async throws {
        let ask = await code { try await alice.addContact(userID: b.userID) }
        XCTAssertEqual(ask, .permissionDenied)
        let open = await code { try await alice.openConversation(peerName: b.name) }
        XCTAssertEqual(open, .permissionDenied)
        let write = await code {
            try await self.send(
                alice, "hi", to: conversationID, members: .direct(peer: b.userID), id: "m2")
        }
        XCTAssertEqual(write, .permissionDenied)
        let own = await code { try await bob.openConversation(peerName: a.name) }
        XCTAssertEqual(own, .failedPrecondition)
        let readd = await code { try await bob.addContact(userID: a.userID) }
        XCTAssertEqual(readd, .failedPrecondition)
    }
}
