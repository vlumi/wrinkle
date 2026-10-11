import XCTest

@testable import WrinkleCore

/// Against a live backend at ws://localhost:8080/v1/ws: accounts, a message each way, a read
/// receipt, a rename reaching the peer. Skipped when nothing listens, unless
/// WRINKLE_TEST_REQUIRES_BACKEND is set.
final class BackendTests: XCTestCase {
    private let url = URL(
        string: ProcessInfo.processInfo.environment["WRINKLE_URL"] ?? "ws://localhost:8080/v1/ws")!

    private func dial() async throws -> Connection {
        let c = Connection(url: url)
        do {
            try await c.open()
        } catch {
            if ProcessInfo.processInfo.environment["WRINKLE_TEST_REQUIRES_BACKEND"] != nil {
                throw error
            }
            throw XCTSkip("no backend at \(url): \(error)")
        }
        return c
    }

    private func next(
        _ events: AsyncStream<Wrinkle_V1_Event>,
        where match: @escaping @Sendable (Wrinkle_V1_Event) -> Bool
    ) async throws -> Wrinkle_V1_Event {
        try await withThrowingTaskGroup(of: Wrinkle_V1_Event.self) { group in
            group.addTask {
                for await e in events where match(e) { return e }
                throw URLError(.cancelled)
            }
            group.addTask {
                try await Task.sleep(for: .seconds(15))
                throw URLError(.timedOut)
            }
            let first = try await group.next()!
            group.cancelAll()
            return first
        }
    }

    private static let password = "correct horse"

    /// A name no earlier run used.
    private func unique(_ prefix: String) -> String {
        "\(prefix)-\(UUID().uuidString.prefix(8).lowercased())"
    }

    /// Registers a new user on a fresh connection and connects it.
    private func registered(_ name: String) async throws -> (Connection, Wrinkle_V1_ConnectResponse)
    {
        let c = try await dial()
        let creds = try await c.register(name: name, password: Self.password, deviceName: "test")
        XCTAssertEqual(creds.userID.count, 16)
        let resp = try await c.connect(deviceToken: creds.deviceToken)
        XCTAssertEqual(resp.user.userID, creds.userID)
        XCTAssertEqual(resp.deviceID, creds.deviceID)
        return (c, resp)
    }

    private func code(_ work: () async throws -> some Any) async -> Wrinkle_V1_ErrorCode? {
        do {
            _ = try await work()
            return nil
        } catch {
            return (error as? BackendError)?.code
        }
    }

    func testMessageEachWayAndReadReceipt() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        XCTAssertNotEqual(a.user.userID, b.user.userID)
        try await alice.registerPushToken(
            service: .apnsSandbox, token: String(repeating: "ab", count: 32))

        // Names are found ignoring case.
        let conv = try await alice.openConversation(peerName: b.user.name.uppercased())
        XCTAssertEqual(conv.conversationID.count, 16)
        XCTAssertEqual(conv.peer.name, b.user.name)
        let sent = try await alice.sendMessage(
            conversationID: conv.conversationID, clientMessageID: "m1", text: "hello bob")
        XCTAssertEqual(sent.seq, 1)

        let got = try await next(bob.events) { $0.message.message.seq == 1 }
        XCTAssertEqual(got.message.message.text, "hello bob")
        XCTAssertEqual(got.message.message.senderUserID, a.user.userID)

        try await bob.markRead(conversationID: conv.conversationID, seq: 1)
        let read = try await next(alice.events) { $0.read.seq == 1 }
        XCTAssertEqual(read.read.userID, b.user.userID)

        let reply = try await bob.sendMessage(
            conversationID: conv.conversationID, clientMessageID: "m2", text: "hi alice")
        XCTAssertEqual(reply.seq, 2)
        _ = try await next(alice.events) { $0.message.message.seq == 2 }
        _ = try await next(bob.events) { $0.message.message.seq == 2 }

        // Replying moved bob's own read position to his reply.
        let list = try await bob.listConversations()
        XCTAssertEqual(list.first?.conversationID, conv.conversationID)
        XCTAssertEqual(list.first?.readSeq, 2)

        let history = try await alice.getMessages(
            conversationID: conv.conversationID, beforeSeq: 0, limit: 10)
        XCTAssertEqual(history.map(\.seq), [2, 1])

        // A rename and an icon reach the peer, and the peer's list shows them.
        let renamed = unique("ios-robert")
        let me = try await bob.updateProfile(name: renamed, icon: "owl")
        XCTAssertEqual(me.userID, b.user.userID)
        XCTAssertEqual(me.name, renamed)
        let profile = try await next(alice.events) { $0.profile.user.name == renamed }
        XCTAssertEqual(profile.profile.user.userID, b.user.userID)
        XCTAssertEqual(profile.profile.user.icon, "owl")
        let peer = try await alice.listConversations().first?.peer
        XCTAssertEqual(peer?.name, renamed)
        XCTAssertEqual(peer?.icon, "owl")

        // A name taken by someone else.
        let taken = await code { try await bob.updateProfile(name: a.user.name) }
        XCTAssertEqual(taken, .alreadyExists)
        await alice.close()
        await bob.close()
    }

    func testAccountErrors() async throws {
        let name = unique("ios-carol")
        let (carol, _) = try await registered(name)
        await carol.close()

        let c = try await dial()
        let duplicate = await code {
            try await c.register(name: name.uppercased(), password: Self.password, deviceName: "")
        }
        XCTAssertEqual(duplicate, .alreadyExists)
        let short = await code {
            try await c.register(name: self.unique("ios-dave"), password: "short", deviceName: "")
        }
        XCTAssertEqual(short, .invalidArgument)
        let wrong = await code {
            try await c.signIn(name: name, password: "wrong password", deviceName: "")
        }
        XCTAssertEqual(wrong, .unauthenticated)
        let badToken = await code { try await c.connect(deviceToken: "v1.nonsense") }
        XCTAssertEqual(badToken, .unauthenticated)

        // Signing in makes a new device; signing it out revokes its token.
        let creds = try await c.signIn(name: name, password: Self.password, deviceName: "test")
        _ = try await c.connect(deviceToken: creds.deviceToken)
        try await c.signOut()
        await c.close()
        let d = try await dial()
        let revoked = await code { try await d.connect(deviceToken: creds.deviceToken) }
        XCTAssertEqual(revoked, .unauthenticated)
        await d.close()
    }
}
