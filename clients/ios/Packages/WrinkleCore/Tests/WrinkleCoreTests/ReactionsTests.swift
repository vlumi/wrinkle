import SwiftData
import XCTest

@testable import WrinkleCore

/// Reactions in the store: events set, replace and remove a user's set, history replaces a
/// message's reactions, deletion drops them, and sealed ones open once the key is held.
@MainActor
final class ReactionsTests: XCTestCase {
    private let conv7 = Data(repeating: 7, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)

    private func makeStore(keys: EpochKeys? = nil) throws -> Store {
        let container = try ModelContainer(
            for: Store.schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        let store = Store(context: ModelContext(container), keys: keys ?? TestSealing.keys(conv7))
        var conv = Wrinkle_V1_Conversation()
        conv.conversationID = conv7
        conv.peer.userID = bob
        _ = store.upsert(conv)
        _ = store.apply(message(1), me: me.hex)
        return store
    }

    private func message(_ seq: UInt64) -> Wrinkle_V1_Event {
        var event = Wrinkle_V1_Event()
        event.message.message = .with {
            $0.conversationID = conv7
            $0.seq = seq
            $0.senderUserID = bob
            $0.clientMessageID = "m\(seq)"
            $0.sentAt = .init(date: Date())
            $0.seal("hi")
        }
        return event
    }

    private func sealed(_ emoji: [String], by user: Data, seq: UInt64 = 1, epoch: UInt32 = 1)
        -> Wrinkle_V1_Sealed
    {
        // A fixed nonce is fine in tests.
        // swiftlint:disable:next force_try
        try! E2EE.seal(
            .with { $0.emoji = emoji }, key: TestSealing.key,
            context: E2EE.ReactionContext(
                conversationID: conv7, epoch: epoch, userID: user, seq: seq),
            nonce: Data(repeating: 2, count: 12))
    }

    private func reaction(_ emoji: [String]?, by user: Data, at t: TimeInterval = 0)
        -> Wrinkle_V1_Event
    {
        var event = Wrinkle_V1_Event()
        event.reaction = .with {
            $0.conversationID = conv7
            $0.seq = 1
            $0.userID = user
            if let emoji { $0.sealed = sealed(emoji, by: user) }
            $0.updatedAt = .init(date: Date(timeIntervalSince1970: 1_800_000_000 + t))
        }
        return event
    }

    private func emoji(_ store: Store) -> [IDHex: [String]] {
        store.reactions(to: 1, in: conv7.hex).reduce(into: [:]) { $0[$1.userID] = $1.emoji }
    }

    func testEventsSetReplaceAndRemove() throws {
        let store = try makeStore()
        XCTAssertNotNil(store.apply(reaction(["👍"], by: bob), me: me.hex))
        _ = store.apply(reaction(["❤️"], by: me, at: 1), me: me.hex)
        XCTAssertEqual(emoji(store), [bob.hex: ["👍"], me.hex: ["❤️"]])
        _ = store.apply(reaction(["😂", "👍"], by: bob, at: 2), me: me.hex)
        XCTAssertEqual(emoji(store)[bob.hex], ["😂", "👍"])

        let chips = ReactionRules.chips(store.reactions(to: 1, in: conv7.hex), me: me.hex)
        XCTAssertEqual(chips.map(\.emoji), ["❤️", "😂", "👍"])
        XCTAssertEqual(chips.map(\.mine), [true, false, false])

        _ = store.apply(reaction(nil, by: bob, at: 3), me: me.hex)
        XCTAssertEqual(emoji(store), [me.hex: ["❤️"]])
        // Removing nothing is no error.
        _ = store.apply(reaction(nil, by: bob, at: 4), me: me.hex)
        XCTAssertEqual(emoji(store).count, 1)
    }

    func testHistoryReplacesAndDeletionDrops() throws {
        let store = try makeStore()
        _ = store.apply(reaction(["👍"], by: bob), me: me.hex)
        var m = message(1).message.message
        m.reactions = [
            .with {
                $0.userID = me
                $0.sealed = sealed(["🙏"], by: me)
                $0.updatedAt = .init(date: Date())
            }
        ]
        store.replaceReactions(m)
        XCTAssertEqual(emoji(store), [me.hex: ["🙏"]])

        var deleted = Wrinkle_V1_Event()
        deleted.messageDeleted = .with {
            $0.conversationID = conv7
            $0.seq = 1
            $0.deletedAt = .init(date: Date())
        }
        _ = store.apply(deleted, me: me.hex)
        XCTAssertTrue(store.reactions(to: 1, in: conv7.hex).isEmpty)
    }

    func testLockedReactionsOpenWithTheKey() throws {
        let keys = EpochKeys(vault: MemoryVault())
        let store = try makeStore(keys: keys)
        _ = store.apply(reaction(["😮"], by: bob), me: me.hex)
        let row = try XCTUnwrap(store.reaction(of: bob.hex, to: 1, in: conv7.hex))
        XCTAssertTrue(row.isLocked)
        XCTAssertEqual(store.locked()[conv7.hex], [1])
        keys.put(conv7, epoch: 1, key: TestSealing.key)
        store.reopen(conv7.hex)
        XCTAssertFalse(row.isLocked)
        XCTAssertEqual(row.emoji, ["😮"])
    }

    func testRules() {
        XCTAssertTrue(ReactionRules.isEmoji("👍"))
        XCTAssertTrue(ReactionRules.isEmoji("❤️"))
        XCTAssertTrue(ReactionRules.isEmoji("👍🏽"))
        XCTAssertFalse(ReactionRules.isEmoji("a"))
        XCTAssertFalse(ReactionRules.isEmoji("1"))
        XCTAssertFalse(ReactionRules.isEmoji("👍👍"))
        XCTAssertFalse(ReactionRules.isEmoji(""))
        XCTAssertEqual(ReactionRules.toggle("👍", in: []), ["👍"])
        XCTAssertEqual(ReactionRules.toggle("👍", in: ["❤️", "👍"]), ["❤️"])
        XCTAssertNil(ReactionRules.toggle("🙏", in: ["❤️", "👍", "😂"]))
        XCTAssertNil(ReactionRules.toggle("x", in: []))
        XCTAssertEqual(ReactionRules.valid(["👍", "x", "👍", "❤️", "😂", "😮"]), ["👍", "❤️", "😂"])
    }
}
