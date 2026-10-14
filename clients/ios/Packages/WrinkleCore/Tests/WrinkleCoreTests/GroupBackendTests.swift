import XCTest

@testable import WrinkleCore

/// A group of three against the live backend: created, a member added, a message, a topic
/// change, read positions, a removal. Members are added only by their contacts: alice and bob
/// are contacts, as are bob and carol.
extension BackendTests {
    /// Polls until the result satisfies the condition, for state the fan-out writes behind
    /// the events.
    private func eventually<T>(
        _ work: () async throws -> T, until ok: (T) -> Bool
    ) async throws -> T {
        for _ in 0..<50 {
            let value = try await work()
            if ok(value) { return value }
            try await Task.sleep(for: .milliseconds(200))
        }
        XCTFail("condition not met in time")
        return try await work()
    }

    private func conversation(_ c: Connection, _ id: Data) async throws
        -> Wrinkle_V1_Conversation?
    {
        try await c.listConversations().first { $0.conversationID == id }
    }

    /// Three users on their own connections.
    private struct Three {
        let alice: Connection, a: Wrinkle_V1_User
        let bob: Connection, b: Wrinkle_V1_User
        let carol: Connection, c: Wrinkle_V1_User
    }

    func testGroupOfThree() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        let (carol, c) = try await registered(unique("ios-carol"))
        let t = Three(alice: alice, a: a.user, bob: bob, b: b.user, carol: carol, c: c.user)
        try await befriend(alice, a.user, bob, b.user)
        try await befriend(bob, b.user, carol, c.user)
        let id = try await create(t)
        try await addAndTalk(t, id)
        try await reads(t, id)
        try await removal(t, id)
        await alice.close()
        await bob.close()
        await carol.close()
    }

    /// Alice creates the group with bob; a retry returns it, bad requests fail.
    private func create(_ t: Three) async throws -> Data {
        let unknown = await code {
            try await t.alice.createGroup(
                topic: "Trip", memberNames: [self.unique("ios-nobody")], clientGroupID: "g0")
        }
        XCTAssertEqual(unknown, .notFound)
        let blank = await code {
            try await t.alice.createGroup(topic: " ", memberNames: [], clientGroupID: "g0")
        }
        XCTAssertEqual(blank, .invalidArgument)
        let stranger = await code {
            try await t.alice.createGroup(
                topic: "Trip", memberNames: [t.c.name], clientGroupID: "g0")
        }
        XCTAssertEqual(stranger, .failedPrecondition)

        let group = try await t.alice.createGroup(
            topic: "Trip", memberNames: [t.b.name.uppercased()], clientGroupID: "g1")
        XCTAssertEqual(group.kind, .group)
        XCTAssertEqual(group.topic, "Trip")
        XCTAssertEqual(group.memberCount, 2)
        XCTAssertTrue(group.member)
        XCTAssertTrue(group.admin)
        XCTAssertEqual(group.lastSeq, 1)
        let id = group.conversationID
        let retry = try await t.alice.createGroup(
            topic: "Trip", memberNames: [t.b.name], clientGroupID: "g1")
        XCTAssertEqual(retry.conversationID, id)

        let created = try await next(t.bob.events) { $0.message.message.conversationID == id }
        let m = created.message.message
        XCTAssertEqual(m.seq, 1)
        XCTAssertEqual(m.senderUserID, t.a.userID)
        XCTAssertEqual(m.notice.groupCreated.topic, "Trip")
        XCTAssertEqual(m.notice.groupCreated.members.map(\.userID), [t.b.userID])
        return id
    }

    /// Bob adds carol, who sees the group from there on; bob writes; carol changes the topic.
    private func addAndTalk(_ t: Three, _ id: Data) async throws {
        try await t.bob.addMembers(conversationID: id, memberNames: [t.c.name])
        let added = try await next(t.carol.events) { $0.message.message.conversationID == id }
        XCTAssertEqual(added.message.message.seq, 2)
        XCTAssertEqual(added.message.message.senderUserID, t.b.userID)
        XCTAssertEqual(added.message.message.notice.membersAdded.users.map(\.name), [t.c.name])
        let seen = try await t.carol.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        XCTAssertEqual(seen.map(\.seq), [2])

        // Adding carol requires a new epoch, which bob's device publishes before sending.
        let hello = try await send(t.bob, "hello all", to: id, members: .group, id: "m1")
        XCTAssertEqual(hello.seq, 3)
        XCTAssertEqual(hello.sealed.epoch, 1)
        for conn in [t.alice, t.carol] {
            let got = try await next(conn.events) { $0.message.message.seq == 3 }
            let text = try await read(conn, got.message.message)
            XCTAssertEqual(text, "hello all")
        }

        try await t.carol.setTopic(conversationID: id, topic: "Holiday")
        for conn in [t.alice, t.bob] {
            let got = try await next(conn.events) { $0.message.message.seq == 4 }
            XCTAssertEqual(got.message.message.notice.topicChanged.topic, "Holiday")
        }
        let renamed = try await eventually({ try await self.conversation(t.alice, id) }) {
            $0?.topic == "Holiday"
        }
        XCTAssertEqual(renamed?.memberCount, 3)
        XCTAssertEqual(renamed?.admin, true)
        let bobs = try await conversation(t.bob, id)
        XCTAssertEqual(bobs?.admin, false)
        XCTAssertEqual(bobs?.hasPeer, false)
    }

    /// Bob, who sent seq 3, hears of alice's read. Carol's own notice at seq 4 already moved
    /// her position there, so her mark changes nothing; GetMembers shows both.
    private func reads(_ t: Three, _ id: Data) async throws {
        try await t.alice.markRead(conversationID: id, seq: 4)
        try await t.carol.markRead(conversationID: id, seq: 4)
        let r = try await next(t.bob.events) {
            $0.read.conversationID == id && $0.read.seq == 4
        }
        XCTAssertEqual(r.read.userID, t.a.userID)
        let members = try await t.bob.getMembers(conversationID: id)
        XCTAssertEqual(Set(members.prefix(2).map(\.user.userID)), [t.a.userID, t.b.userID])
        XCTAssertEqual(members.last?.user.userID, t.c.userID)
        XCTAssertEqual(members.last?.joinedSeq, 2)
        let positions = Dictionary(uniqueKeysWithValues: members.map { ($0.user.name, $0.readSeq) })
        XCTAssertEqual(positions[t.a.name], 4)
        XCTAssertEqual(positions[t.c.name], 4)
        XCTAssertEqual(members.filter(\.admin).map(\.user.userID), [t.a.userID])
    }

    /// Only the admin removes; carol keeps her history up to the removal and can no longer
    /// write.
    private func removal(_ t: Three, _ id: Data) async throws {
        let denied = await code {
            try await t.bob.removeMember(conversationID: id, userID: t.c.userID)
        }
        XCTAssertEqual(denied, .permissionDenied)
        try await t.alice.removeMember(conversationID: id, userID: t.c.userID)
        let removed = try await next(t.carol.events) { $0.message.message.seq == 5 }
        XCTAssertEqual(removed.message.message.notice.memberRemoved.user.userID, t.c.userID)
        let refused = await code {
            try await self.send(t.carol, "hi", to: id, members: .group, id: "m2")
        }
        XCTAssertEqual(refused, .permissionDenied)
        // The others' next epoch leaves carol out.
        let after = try await send(t.alice, "without carol", to: id, members: .group, id: "m3")
        XCTAssertEqual(after.sealed.epoch, 2)
        let got = try await next(t.bob.events) { $0.message.message.seq == after.seq }
        let text = try await read(t.bob, got.message.message)
        XCTAssertEqual(text, "without carol")
        let unreadable = try await read(t.carol, after)
        XCTAssertNil(unreadable)
        let history = try await t.carol.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        XCTAssertEqual(history.map(\.seq), [5, 4, 3, 2])
        let former = try await eventually({ try await self.conversation(t.carol, id) }) {
            $0?.member == false
        }
        XCTAssertEqual(former?.admin, false)
        let remaining = try await t.alice.getMembers(conversationID: id)
        XCTAssertEqual(remaining.count, 2)
    }
}
