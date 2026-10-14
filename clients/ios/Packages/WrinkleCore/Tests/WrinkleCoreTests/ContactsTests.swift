import XCTest

@testable import WrinkleCore

/// The contact book: listed states, events that change and end them, the blocked users and
/// what is hidden because of them, and the contacts offered for groups.
@MainActor
final class ContactsTests: XCTestCase {
    private let bob = Data(repeating: 2, count: 16)
    private let carol = Data(repeating: 3, count: 16)
    private let dave = Data(repeating: 4, count: 16)
    private let erin = Data(repeating: 5, count: 16)

    private func contact(
        _ id: Data, _ name: String, _ state: Wrinkle_V1_ContactState
    ) -> Wrinkle_V1_Contact {
        .with {
            $0.user.userID = id
            $0.user.name = name
            $0.state = state
        }
    }

    private func listed() -> ContactBook {
        ContactBook([
            contact(bob, "bob", .contact), contact(carol, "Carol", .incoming),
            contact(dave, "dave", .outgoing), contact(erin, "erin", .blocked),
        ])
    }

    func testListedStates() {
        let book = listed()
        XCTAssertEqual(book.state(of: bob.hex), .contact)
        XCTAssertEqual(book.state(of: carol.hex), .incoming)
        XCTAssertEqual(book.state(of: dave.hex), .outgoing)
        XCTAssertEqual(book.state(of: erin.hex), .blocked)
        XCTAssertEqual(book.state(of: Data(repeating: 9, count: 16).hex), .unspecified)
        XCTAssertEqual(book.incomingCount, 1)
        XCTAssertEqual(book.blocked, [erin.hex])
        XCTAssertTrue(book.isBlocked(erin.hex))
        XCTAssertFalse(book.isBlocked(bob.hex))
    }

    func testEventsChangeAndEndStates() {
        var book = listed()
        // Carol's request accepted, dave's withdrawn, erin unblocked.
        book.apply(contact(carol, "Carol", .contact))
        book.apply(contact(dave, "dave", .unspecified))
        book.apply(contact(erin, "erin", .unspecified))
        XCTAssertEqual(book.users(.contact).map(\.name), ["bob", "Carol"])
        XCTAssertEqual(book.state(of: dave.hex), .unspecified)
        XCTAssertNil(book.entries[dave.hex])
        XCTAssertEqual(book.blocked, [])
        XCTAssertEqual(book.incomingCount, 0)
        // A contact blocked: the profile stays, the state changes.
        book.set(.blocked, for: bob.hex)
        XCTAssertEqual(book.users(.blocked).map(\.name), ["bob"])
        // Someone unknown blocked from a group: the given profile is kept.
        let frank = Data(repeating: 6, count: 16)
        book.set(
            .blocked, for: frank.hex,
            user: .with {
                $0.userID = frank; $0.name = "frank"
            })
        XCTAssertEqual(book.users(.blocked).map(\.name), ["bob", "frank"])
        book.set(.unspecified, for: frank.hex)
        XCTAssertEqual(book.blocked, [bob.hex])
    }

    func testBlockedSendersHiddenInGroupsOnly() {
        let book = listed()
        XCTAssertTrue(book.hides(erin.hex, inGroup: true))
        XCTAssertFalse(book.hides(erin.hex, inGroup: false))
        XCTAssertFalse(book.hides(bob.hex, inGroup: true))
    }

    func testPickableContacts() {
        var book = listed()
        book.apply(contact(carol, "Carol", .contact))
        var gone = contact(dave, "", .contact)
        gone.user.deleted = true
        book.apply(gone)
        XCTAssertEqual(book.pickable("").map(\.name), ["bob", "Carol"])
        XCTAssertEqual(book.pickable(" car ").map(\.name), ["Carol"])
        XCTAssertEqual(book.pickable("", excluding: [bob.hex]).map(\.name), ["Carol"])
        XCTAssertEqual(book.pickable("zed"), [])
    }

    func testDirectConversationWithAUser() throws {
        let store = try Store.inMemory()
        let direct = Data(repeating: 7, count: 16)
        let group = Data(repeating: 8, count: 16)
        _ = store.upsert(
            .with {
                $0.conversationID = direct
                $0.peer.userID = bob
            })
        _ = store.upsert(
            .with {
                $0.conversationID = group
                $0.kind = .group
                $0.topic = "Trip"
            })
        XCTAssertEqual(store.directConversation(with: bob.hex)?.id, direct.hex)
        XCTAssertNil(store.directConversation(with: carol.hex))
        XCTAssertNil(store.directConversation(with: ""))
    }
}
