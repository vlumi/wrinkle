import XCTest

@testable import WrinkleCore

/// Against a live backend at ws://localhost:8080/v1/ws: two devices, a message each way, a
/// read receipt. Skipped when nothing listens, unless WRINKLE_TEST_REQUIRES_BACKEND is set.
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

    func testMessageEachWayAndReadReceipt() async throws {
        let suffix = String(UInt64(Date().timeIntervalSince1970 * 1000), radix: 36)
        let alice = try await dial()
        let bob = try await dial()
        let a = try await alice.connect(
            userName: "ios-alice-\(suffix)", deviceID: 0, deviceName: "test")
        let b = try await bob.connect(
            userName: "ios-bob-\(suffix)", deviceID: 0, deviceName: "test")
        XCTAssertNotEqual(a.user.userID, b.user.userID)
        try await alice.registerPushToken(
            service: .apnsSandbox, token: String(repeating: "ab", count: 32))

        let conv = try await alice.openConversation(peerUserName: b.user.name)
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

        let list = try await bob.listConversations()
        XCTAssertEqual(list.first?.conversationID, conv.conversationID)
        XCTAssertEqual(list.first?.readSeq, 1)

        let history = try await alice.getMessages(
            conversationID: conv.conversationID, beforeSeq: 0, limit: 10)
        XCTAssertEqual(history.map(\.seq), [2, 1])
        await alice.close()
        await bob.close()
    }
}
