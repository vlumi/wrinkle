import SwiftData
import XCTest

@testable import WrinkleCore

@MainActor
final class GroupsTests: XCTestCase {
    private let group = Data(repeating: 9, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)
    private let carol = Data(repeating: 3, count: 16)

    private func makeStore() throws -> Store {
        let container = try ModelContainer(
            for: Store.schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        return Store(context: ModelContext(container), keys: TestSealing.keys(group))
    }

    private func user(_ id: Data, _ name: String) -> Wrinkle_V1_User {
        .with {
            $0.userID = id
            $0.name = name
        }
    }

    private func notice(
        _ seq: UInt64, from sender: Data, _ kind: Wrinkle_V1_Notice.OneOf_Kind
    ) -> Wrinkle_V1_Event {
        var e = Wrinkle_V1_Event()
        e.message.message = .with {
            $0.conversationID = group
            $0.seq = seq
            $0.senderUserID = sender
            $0.clientMessageID = "notice-\(seq)"
            $0.notice = .with { $0.kind = kind }
        }
        return e
    }

    private func text(_ seq: UInt64, from sender: Data, _ text: String) -> Wrinkle_V1_Event {
        var e = Wrinkle_V1_Event()
        e.message.message = .with {
            $0.conversationID = group
            $0.seq = seq
            $0.senderUserID = sender
            $0.clientMessageID = "m\(seq)"
            $0.seal(text)
        }
        return e
    }

    /// A group bob created with me and carol, as the list first shows it.
    private func listed(_ store: Store) -> StoredConversation {
        store.upsert(
            .with {
                $0.conversationID = group
                $0.kind = .group
                $0.topic = "Trip"
                $0.memberCount = 3
                $0.member = true
            })
    }

    func testListedGroup() throws {
        let store = try makeStore()
        let c = listed(store)
        XCTAssertTrue(c.isGroup)
        XCTAssertFalse(c.isFormer)
        XCTAssertEqual(c.title, "Trip")
        XCTAssertEqual(c.memberCount, 3)
        XCTAssertFalse(c.admin)
        XCTAssertEqual(c.peerID, "")
    }

    func testNoticesFollowMembershipAndTopic() throws {
        let store = try makeStore()
        let c = listed(store)
        let created = Wrinkle_V1_Notice.OneOf_Kind.groupCreated(
            .with {
                $0.topic = "Trip"
                $0.members = [user(me, "me"), user(carol, "carol")]
            })
        _ = store.apply(notice(1, from: bob, created), me: me.hex)
        XCTAssertEqual(
            c.lastNotice.flatMap { try? Wrinkle_V1_Notice(serializedBytes: $0) }?
                .groupCreated.topic, "Trip")
        XCTAssertNil(c.lastText.flatMap { $0.isEmpty ? nil : $0 })
        XCTAssertTrue(c.member)
        XCTAssertFalse(c.admin)
        XCTAssertEqual(c.memberCount, 3)
        XCTAssertEqual(store.member(bob.hex, in: c.id)?.admin, true)
        XCTAssertEqual(store.name(of: carol.hex, in: c.id), "carol")
        // The creator's name is not in the notice.
        XCTAssertNil(store.name(of: bob.hex, in: c.id))

        _ = store.apply(text(2, from: bob, "hi"), me: me.hex)
        XCTAssertNil(c.lastNotice)
        XCTAssertEqual(c.lastText, "hi")

        _ = store.apply(
            notice(3, from: carol, .topicChanged(.with { $0.topic = "Holiday" })), me: me.hex)
        XCTAssertEqual(c.title, "Holiday")

        _ = store.apply(
            notice(
                4, from: bob,
                .adminChanged(
                    .with {
                        $0.user = user(me, "me")
                        $0.admin = true
                    })), me: me.hex)
        XCTAssertTrue(c.admin)

        // Bob leaves; I stay admin, nobody is promoted.
        _ = store.apply(notice(5, from: bob, .memberLeft(.init())), me: me.hex)
        XCTAssertEqual(c.memberCount, 2)
        XCTAssertEqual(store.member(bob.hex, in: c.id)?.current, false)

        _ = store.apply(
            notice(6, from: carol, .membersAdded(.with { $0.users = [user(bob, "bob")] })),
            me: me.hex)
        XCTAssertEqual(c.memberCount, 3)
        XCTAssertEqual(store.member(bob.hex, in: c.id)?.current, true)
        XCTAssertEqual(store.member(bob.hex, in: c.id)?.joinedSeq, 6)
        XCTAssertEqual(store.name(of: bob.hex, in: c.id), "bob")

        // Removed: a former member, history kept.
        _ = store.apply(
            notice(7, from: carol, .memberRemoved(.with { $0.user = user(me, "me") })), me: me.hex)
        XCTAssertTrue(c.isFormer)
        XCTAssertFalse(c.admin)
        XCTAssertEqual(c.memberCount, 2)
        XCTAssertEqual(try store.context.fetchCount(FetchDescriptor<StoredMessage>()), 7)
    }

    func testOlderNoticeDoesNotUndoNewerState() throws {
        let store = try makeStore()
        let c = listed(store)
        _ = store.apply(
            notice(5, from: bob, .topicChanged(.with { $0.topic = "New" })), me: me.hex)
        _ = store.apply(
            notice(4, from: bob, .topicChanged(.with { $0.topic = "Old" })), me: me.hex)
        XCTAssertEqual(c.topic, "New")
        // A list response older than what events showed leaves the topic.
        _ = store.upsert(
            .with {
                $0.conversationID = group
                $0.kind = .group
                $0.topic = "Stale"
                $0.lastSeq = 3
                $0.member = true
            })
        XCTAssertEqual(c.topic, "New")
    }

    func testPromotionOnLeave() throws {
        let store = try makeStore()
        let c = listed(store)
        _ = store.apply(
            notice(2, from: bob, .memberLeft(.with { $0.promoted = user(me, "me") })), me: me.hex)
        XCTAssertTrue(c.admin)
        XCTAssertEqual(store.member(me.hex, in: c.id)?.admin, true)
    }

    func testMembersAndGroupReadPositions() throws {
        let store = try makeStore()
        let c = listed(store)
        store.replaceMembers(
            [
                .with {
                    $0.user = user(bob, "bob")
                    $0.admin = true
                    $0.joinedSeq = 1
                    $0.readSeq = 4
                },
                .with {
                    $0.user = user(me, "me")
                    $0.joinedSeq = 1
                    $0.readSeq = 5
                },
                .with {
                    $0.user = user(carol, "carol")
                    $0.joinedSeq = 3
                },
            ], in: c.id, me: me.hex)
        XCTAssertEqual(c.memberCount, 3)
        XCTAssertFalse(c.admin)
        // Those who joined together, by id, as the backend orders them.
        XCTAssertEqual(store.members(of: c.id).map(\.userID), [me.hex, bob.hex, carol.hex])

        var read = Wrinkle_V1_Event()
        read.read = .with {
            $0.conversationID = group
            $0.userID = carol
            $0.seq = 5
        }
        _ = store.apply(read, me: me.hex)
        XCTAssertEqual(store.member(carol.hex, in: c.id)?.readSeq, 5)
        XCTAssertEqual(c.peerReadSeq, 0)

        // A refetch without carol keeps her as a former member, named.
        store.replaceMembers(
            [
                .with {
                    $0.user = user(bob, "bob")
                    $0.admin = true
                },
                .with { $0.user = user(me, "me") },
            ], in: c.id, me: me.hex)
        XCTAssertEqual(c.memberCount, 2)
        XCTAssertEqual(store.member(carol.hex, in: c.id)?.current, false)
        XCTAssertEqual(store.name(of: carol.hex, in: c.id), "carol")

        // A profile event renames the member.
        var profile = Wrinkle_V1_Event()
        profile.profile.user = user(bob, "Robert")
        _ = store.apply(profile, me: me.hex)
        XCTAssertEqual(store.name(of: bob.hex, in: c.id), "Robert")
    }

    func testNoticeSentences() {
        let m = me.hex
        let alice = user(Data(repeating: 4, count: 16), "Alice")
        let b = user(bob, "Bob")
        let cc = user(carol, "Carol")
        func say(_ kind: Wrinkle_V1_Notice.OneOf_Kind, from sender: IDHex = alice.userID.hex)
            -> String
        {
            NoticeText.sentence(
                .with { $0.kind = kind }, senderID: sender,
                sender: sender == alice.userID.hex ? "Alice" : nil, me: m)
        }
        XCTAssertEqual(
            say(.membersAdded(.with { $0.users = [b, cc] })), "Alice added Bob and Carol")
        XCTAssertEqual(
            say(.membersAdded(.with { $0.users = [b, cc, user(me, "me")] })),
            "Alice added Bob, Carol and you")
        XCTAssertEqual(
            say(.groupCreated(.with { $0.members = [b] })), "Alice created the group with Bob")
        XCTAssertEqual(say(.groupCreated(.init()), from: m), "You created the group")
        XCTAssertEqual(
            say(.memberLeft(.with { $0.promoted = cc }), from: bob.hex),
            "Someone left; Carol is now an admin")
        XCTAssertEqual(
            say(.memberLeft(.with { $0.promoted = user(me, "me") })),
            "Alice left; you are now an admin")
        XCTAssertEqual(
            say(.topicChanged(.with { $0.topic = "Trip" })), "Alice changed the topic to “Trip”")
        XCTAssertEqual(
            say(
                .adminChanged(
                    .with {
                        $0.user = b
                        $0.admin = true
                    })), "Alice made Bob an admin")
        XCTAssertEqual(
            say(.memberRemoved(.with { $0.user = user(carol, "") })), "Alice removed someone")
        XCTAssertEqual(
            say(
                .memberRemoved(
                    .with {
                        $0.user = .with {
                            $0.userID = carol
                            $0.deleted = true
                        }
                    })), "Alice removed Deleted account")
    }
}
