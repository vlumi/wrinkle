import XCTest

@testable import WrinkleCore

/// Against a live backend at ws://localhost:8080/v1/ws: accounts, a message each way, a read
/// receipt, a rename reaching the peer, an avatar upload, devices and remote sign-out, account
/// deletion, a group of three (GroupBackendTests.swift), deleting a message
/// (DeletionBackendTests.swift), end-to-end encryption (E2EEBackendTests.swift), reactions
/// (ReactionBackendTests.swift), stickers (StickerBackendTests.swift), attachments
/// (AttachmentBackendTests.swift). Every message
/// is sealed on the sending device and opened on the receiving ones. Skipped when nothing listens, unless
/// WRINKLE_TEST_REQUIRES_BACKEND is set.
final class BackendTests: XCTestCase {
    let url = URL(
        string: ProcessInfo.processInfo.environment["WRINKLE_URL"] ?? "ws://localhost:8080/v1/ws")!

    func dial() async throws -> Connection {
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

    func next(
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

    static let password = "correct horse"

    /// Each connected device's keys, by connection.
    var keyManagers: [ObjectIdentifier: KeyManager] = [:]

    /// A name no earlier run used.
    func unique(_ prefix: String) -> String {
        "\(prefix)-\(UUID().uuidString.prefix(8).lowercased())"
    }

    /// Registers a new user on a fresh connection and connects it.
    func registered(_ name: String) async throws -> (Connection, Wrinkle_V1_ConnectResponse) {
        let c = try await dial()
        let creds = try await c.register(name: name, password: Self.password, deviceName: "test")
        XCTAssertEqual(creds.userID.count, 16)
        let resp = try await c.connect(deviceToken: creds.deviceToken)
        XCTAssertEqual(resp.user.userID, creds.userID)
        XCTAssertEqual(resp.deviceID, creds.deviceID)
        try await keyed(c, resp)
        return (c, resp)
    }

    func code(_ work: () async throws -> some Any) async -> Wrinkle_V1_ErrorCode? {
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
        let sent = try await send(
            alice, "hello bob", to: conv.conversationID, members: .direct(peer: b.user.userID),
            id: "m1")
        XCTAssertEqual(sent.seq, 1)

        let got = try await next(bob.events) { $0.message.message.seq == 1 }
        let text = try await read(bob, got.message.message)
        XCTAssertEqual(text, "hello bob")
        XCTAssertEqual(got.message.message.senderUserID, a.user.userID)

        try await bob.markRead(conversationID: conv.conversationID, seq: 1)
        let read = try await next(alice.events) { $0.read.seq == 1 }
        XCTAssertEqual(read.read.userID, b.user.userID)

        let reply = try await send(
            bob, "hi alice", to: conv.conversationID, members: .direct(peer: a.user.userID),
            id: "m2")
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

    /// Waits for the server to close the connection; returns its close code.
    private func closed(_ c: Connection) async throws -> Int? {
        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask { for await _ in c.events {} }
            group.addTask {
                try await Task.sleep(for: .seconds(5))
                throw URLError(.timedOut)
            }
            try await group.next()
            group.cancelAll()
        }
        return await c.closeCode
    }

    private func scratchBlobs() -> Blobs {
        Blobs(
            directory: FileManager.default.temporaryDirectory.appending(
                path: "wrinkle-blobs-\(UUID().uuidString)"))
    }

    func testAvatarUploadRoundTrip() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        let (bob, _) = try await registered(unique("ios-bob"))
        try await converse(bob, with: a.user.name)
        let base = try XCTUnwrap(BlobAddress.base(gateway: url))
        let jpeg = try XCTUnwrap(AvatarImage.jpeg(from: testPNG(width: 900, height: 700)))

        let gif = await code {
            try await alice.createUpload(purpose: .avatar, mediaType: "image/gif", size: 10)
        }
        XCTAssertEqual(gif, .invalidArgument)
        let large = await code {
            try await alice.createUpload(
                purpose: .avatar, mediaType: AvatarImage.mediaType, size: AvatarImage.maxBytes + 1)
        }
        XCTAssertEqual(large, .invalidArgument)

        let slot = try await alice.createUpload(
            purpose: .avatar, mediaType: AvatarImage.mediaType, size: jpeg.count)
        XCTAssertEqual(slot.blobID.count, 16)
        XCTAssertTrue(slot.uploadPath.hasPrefix("/blobs/\(slot.blobID.hex)?"))
        let early = await code { try await alice.updateProfile(avatar: slot.blobID) }
        XCTAssertEqual(early, .failedPrecondition)

        try await scratchBlobs().upload(
            jpeg, blob: slot.blobID, uploadPath: slot.uploadPath,
            mediaType: AvatarImage.mediaType, base: base)
        let foreign = await code { try await bob.updateProfile(avatar: slot.blobID) }
        XCTAssertEqual(foreign, .failedPrecondition)
        let me = try await alice.updateProfile(avatar: slot.blobID)
        XCTAssertEqual(me.avatar, slot.blobID)
        let profile = try await next(bob.events) { $0.profile.user.avatar == slot.blobID }
        XCTAssertEqual(profile.profile.user.userID, a.user.userID)
        let listed = try await bob.listConversations().first?.peer
        XCTAssertEqual(listed?.avatar, slot.blobID)

        // Read back over plain HTTP, into an empty cache.
        let read = try await scratchBlobs().data(blob: slot.blobID, base: base)
        XCTAssertEqual(read, jpeg)

        let cleared = try await alice.updateProfile(avatar: Data())
        XCTAssertTrue(cleared.avatar.isEmpty)
        await alice.close()
        await bob.close()
    }

    func testDevicesAndRemoteSignOut() async throws {
        let name = unique("ios-erin")
        let (first, f) = try await registered(name)
        let second = try await dial()
        let creds = try await second.signIn(name: name, password: Self.password, deviceName: "two")
        _ = try await second.connect(deviceToken: creds.deviceToken)

        let signedIn = try await next(first.events) { $0.device.device.deviceID == creds.deviceID }
        XCTAssertFalse(signedIn.device.signedOut)
        XCTAssertEqual(signedIn.device.device.name, "two")
        let devices = try await first.listDevices()
        XCTAssertEqual(devices.map(\.deviceID), [f.deviceID, creds.deviceID])

        let renamed = try await first.renameDevice(id: creds.deviceID, name: "renamed")
        XCTAssertEqual(renamed.name, "renamed")
        _ = try await next(first.events) { $0.device.device.name == "renamed" }

        let own = await code { try await first.signOutDevice(id: f.deviceID) }
        XCTAssertEqual(own, .invalidArgument)
        try await first.signOutDevice(id: creds.deviceID)
        let closeCode = try await closed(second)
        XCTAssertEqual(closeCode, CloseCode.signedOut)
        let gone = try await next(first.events) {
            $0.device.device.deviceID == creds.deviceID && $0.device.signedOut
        }
        XCTAssertTrue(gone.device.signedOut)
        let again = await code { try await first.signOutDevice(id: creds.deviceID) }
        XCTAssertEqual(again, .notFound)

        let d = try await dial()
        let revoked = await code { try await d.connect(deviceToken: creds.deviceToken) }
        XCTAssertEqual(revoked, .unauthenticated)
        await d.close()
        await first.close()
    }

    func testDeleteAccount() async throws {
        let name = unique("ios-frank")
        let (frank, f) = try await registered(name)
        let (grace, _) = try await registered(unique("ios-grace"))
        try await converse(grace, with: name)

        let wrong = await code { try await frank.deleteAccount(password: "wrong password") }
        XCTAssertEqual(wrong, .unauthenticated)
        do {
            try await frank.deleteAccount(password: Self.password)
        } catch {
            // The close that follows may beat the response, but no error response.
            XCTAssertNil(error as? BackendError)
        }
        let closeCode = try await closed(frank)
        XCTAssertEqual(closeCode, CloseCode.signedOut)

        let deleted = try await next(grace.events) {
            $0.profile.user.userID == f.user.userID && $0.profile.user.deleted
        }
        XCTAssertTrue(deleted.profile.user.name.isEmpty)
        let peer = try await grace.listConversations().first?.peer
        XCTAssertEqual(peer?.deleted, true)
        XCTAssertEqual(peer?.name, "")

        let c = try await dial()
        let signIn = await code {
            try await c.signIn(name: name, password: Self.password, deviceName: "")
        }
        XCTAssertEqual(signIn, .unauthenticated)
        // The name is free again.
        _ = try await c.register(name: name, password: Self.password, deviceName: "")
        await c.close()
        await grace.close()
    }
}

extension BackendTests {
    /// Opens a conversation and sends a message, so that both are in each other's lists.
    fileprivate func converse(_ c: Connection, with peer: String) async throws {
        let conv = try await c.openConversation(peerName: peer)
        try await send(
            c, "hello", to: conv.conversationID, members: .direct(peer: conv.peer.userID),
            id: "hello")
    }
}
