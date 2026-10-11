import CryptoKit
import XCTest

@testable import WrinkleCore

/// End-to-end encryption against the live backend: two users exchange sealed messages and the
/// server's copies open only on their devices; a new device of a user gets the history once
/// another of the user's devices shares it; both users see the same safety number.
extension BackendTests {
    /// Gives the connected device a new key pair, published, and an empty key store.
    @discardableResult
    func keyed(_ c: Connection, _ resp: Wrinkle_V1_ConnectResponse) async throws -> KeyManager {
        let km = KeyManager(
            userID: resp.user.userID, deviceID: resp.deviceID,
            privateKey: Curve25519.KeyAgreement.PrivateKey(),
            epochs: EpochKeys(vault: MemoryVault()))
        try await km.publish(c)
        keyManagers[ObjectIdentifier(c)] = km
        return km
    }

    func keys(_ c: Connection) throws -> KeyManager {
        try XCTUnwrap(keyManagers[ObjectIdentifier(c)], "connection has no keys")
    }

    /// Sends a text, sealed by the connection's device.
    @discardableResult
    func send(
        _ c: Connection, _ text: String, to conversationID: Data,
        members: KeyManager.Members, id: String
    ) async throws -> Wrinkle_V1_Message {
        try await keys(c).send(text, to: conversationID, members: members, clientMessageID: id, c)
    }

    /// The text of a message as the connection's device opens it, fetching keys it lacks; nil
    /// if it cannot.
    func read(_ c: Connection, _ m: Wrinkle_V1_Message) async throws -> String? {
        let km = try keys(c)
        if let content = km.open(m) { return content.text }
        try await km.fetchKeys(m.conversationID, c, after: 0)
        return km.open(m)?.text
    }

    func testEncryptedExchange() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        let conv = try await alice.openConversation(peerName: b.user.name)
        let id = conv.conversationID

        let sent = try await send(
            alice, "sealed hello", to: id, members: .direct(peer: b.user.userID), id: "e1")
        XCTAssertEqual(sent.sealed.epoch, 1)
        XCTAssertEqual(sent.sealed.nonce.count, 12)
        XCTAssertFalse(sent.sealed.ciphertext.contains(Data("sealed hello".utf8)))
        let got = try await next(bob.events) { $0.message.message.seq == sent.seq }
        let opened = try await read(bob, got.message.message)
        XCTAssertEqual(opened, "sealed hello")

        let reply = try await send(
            bob, "sealed reply", to: id, members: .direct(peer: a.user.userID), id: "e2")
        XCTAssertEqual(reply.sealed.epoch, 1)

        for (c, peer) in [(alice, b.user.userID), (bob, a.user.userID)] {
            try await serverCopiesOpen(c, in: id, with: peer, last: reply)
        }

        // Content moved to another message does not open.
        var moved = got.message.message
        moved.clientMessageID = "e2"
        let movedText = try await read(bob, moved)
        XCTAssertNil(movedText)

        // An epoch other than the current one is refused.
        let stale = await code {
            try await alice.sendMessage(
                conversationID: id, clientMessageID: "e3",
                sealed: .with {
                    $0.epoch = 9
                    $0.nonce = Data(count: 12)
                    $0.ciphertext = Data(count: 32)
                })
        }
        XCTAssertEqual(stale, .failedPrecondition)
        try await sameSafetyNumber(alice, a.user.userID, bob, b.user.userID)
        await alice.close()
        await bob.close()
    }

    /// The server's copies of the conversation, from history and the list, open on the device.
    private func serverCopiesOpen(
        _ c: Connection, in id: Data, with peer: Data, last reply: Wrinkle_V1_Message
    ) async throws {
        let history = try await c.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        var texts: [String?] = []
        for m in history { texts.append(try await read(c, m)) }
        XCTAssertEqual(texts, ["sealed reply", "sealed hello"])
        // The list follows the fan-out.
        var listed: Wrinkle_V1_Conversation?
        for _ in 0..<50 {
            listed = try await c.listConversations().first { $0.peer.userID == peer }
            if listed?.lastMessage.seq == reply.seq { break }
            try await Task.sleep(for: .milliseconds(100))
        }
        let last = try await read(c, try XCTUnwrap(listed?.lastMessage))
        XCTAssertEqual(last, "sealed reply")
    }

    /// Both users see the same safety number.
    private func sameSafetyNumber(_ alice: Connection, _ a: Data, _ bob: Connection, _ b: Data)
        async throws
    {
        let ka = try keys(alice)
        let kb = try keys(bob)
        let numberA = E2EE.safetyNumber(
            try await ka.fingerprint(of: a, alice, refresh: true),
            try await ka.fingerprint(of: b, alice, refresh: true))
        let numberB = E2EE.safetyNumber(
            try await kb.fingerprint(of: b, bob, refresh: true),
            try await kb.fingerprint(of: a, bob, refresh: true))
        XCTAssertEqual(numberA, numberB)
        XCTAssertEqual(numberA.count, 35)
    }

    func testHistoryForNewDevice() async throws {
        let name = unique("ios-alice")
        let (alice, a) = try await registered(name)
        let (bob, _) = try await registered(unique("ios-bob"))
        let conv = try await bob.openConversation(peerName: name)
        let id = conv.conversationID
        let old = try await send(
            bob, "before the new device", to: id, members: .direct(peer: a.user.userID), id: "h1")

        // Alice signs in on a second device, which sets its key.
        let second = try await dial()
        let creds = try await second.signIn(name: name, password: Self.password, deviceName: "two")
        let resp = try await second.connect(deviceToken: creds.deviceToken)
        let secondKeys = try await keyed(second, resp)

        // Her first device and bob hear of the key; bob only the id and key.
        let own = try await next(alice.events) {
            $0.device.device.deviceID == creds.deviceID && !$0.device.device.publicKey.isEmpty
        }
        XCTAssertEqual(own.device.userID, a.user.userID)
        XCTAssertEqual(own.device.device.publicKey, secondKeys.publicKey)
        XCTAssertEqual(own.device.device.name, "two")
        let peers = try await next(bob.events) { $0.device.device.deviceID == creds.deviceID }
        XCTAssertEqual(peers.device.userID, a.user.userID)
        XCTAssertEqual(peers.device.device.publicKey, secondKeys.publicKey)
        XCTAssertTrue(peers.device.device.name.isEmpty)

        let history = try await second.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        XCTAssertEqual(history.map(\.seq), [old.seq])
        let before = try await read(second, try XCTUnwrap(history.first))
        XCTAssertNil(before)

        // The first device shares its keys; the history opens on the second.
        let shared = try await keys(alice).shareHistory(
            withDevice: creds.deviceID, publicKey: own.device.device.publicKey, alice)
        XCTAssertEqual(shared, 0)  // Alice's first device has fetched no keys yet.
        _ = try await read(alice, old)
        let sharedNow = try await keys(alice).shareHistory(
            withDevice: creds.deviceID, publicKey: own.device.device.publicKey, alice)
        XCTAssertEqual(sharedNow, 1)
        let after = try await read(second, try XCTUnwrap(history.first))
        XCTAssertEqual(after, "before the new device")

        // Sharing the current epoch made the second device one of its recipients, so bob,
        // told of the device, keeps sending in it.
        try await keys(bob).noteDevice(
            user: peers.device.userID, peers.device.device, signedOut: false)
        let third = try await send(
            bob, "to both devices", to: id, members: .direct(peer: a.user.userID), id: "h2")
        XCTAssertEqual(third.sealed.epoch, 1)
        let onSecond = try await read(second, third)
        XCTAssertEqual(onSecond, "to both devices")
        let onFirst = try await read(alice, third)
        XCTAssertEqual(onFirst, "to both devices")
        try await signOutRotates(alice, a.user.userID, second: creds.deviceID, bob, in: id)
        await second.close()
        await alice.close()
        await bob.close()
    }

    /// Once the second device signs out, bob's next message goes to a new epoch without it.
    private func signOutRotates(
        _ alice: Connection, _ a: Data, second: UInt64, _ bob: Connection, in id: Data
    ) async throws {
        try await alice.signOutDevice(id: second)
        let gone = try await next(bob.events) {
            $0.device.device.deviceID == second && $0.device.signedOut
        }
        XCTAssertEqual(gone.device.userID, a)
        try await keys(bob).noteDevice(
            user: gone.device.userID, gone.device.device, signedOut: true)
        let fourth = try await send(
            bob, "to the first device", to: id, members: .direct(peer: a), id: "h3")
        XCTAssertEqual(fourth.sealed.epoch, 2)
        let onlyFirst = try await read(alice, fourth)
        XCTAssertEqual(onlyFirst, "to the first device")
    }
}
