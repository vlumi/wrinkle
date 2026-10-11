import XCTest

@testable import WrinkleCore

/// Attachments against the live backend: a picture prepared, encrypted and uploaded by the
/// sender, sent with its blob listed, received with the blob id, downloaded, checked and
/// decrypted by the peer to the bytes sent; a blob not uploaded is refused; a resend returns
/// the same message; deleting it clears the blob ids.
extension BackendTests {
    private func files() -> AttachmentFiles {
        let dir = FileManager.default.temporaryDirectory.appending(
            path: "wrinkle-attachments-\(UUID().uuidString)")
        return AttachmentFiles(
            directory: dir, blobs: Blobs(directory: dir.appending(path: "blobs")))
    }

    func testAttachmentRoundTrip() async throws {
        let (alice, _) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        let conv = try await alice.openConversation(peerName: b.user.name)
        let id = conv.conversationID
        let members = KeyManager.Members.direct(peer: b.user.userID)
        let base = try XCTUnwrap(BlobAddress.base(gateway: url))

        let picked = OutgoingFile(
            data: try testPNG(width: 1200, height: 900), name: "view.png", mediaType: "image/png")
        let prepared = AttachmentRules.prepare(
            picked,
            thumbnailBytes: AttachmentRules.thumbnailBudget(
                count: 1, caption: "look", names: [picked.name]))
        let attachment = try await files().upload(prepared, alice, base: base)
        XCTAssertEqual(attachment.width, 1200)
        XCTAssertFalse(attachment.thumbnail.isEmpty)

        // A slot never uploaded to is refused.
        let empty = try await alice.createUpload(
            purpose: .attachment, mediaType: AttachmentRules.blobMediaType, size: 100)
        let notUploaded = await code {
            try await self.keys(alice).send(
                content: .with { $0.text = "x" }, to: id, members: members,
                clientMessageID: "a0", blobIDs: [empty.blobID], alice)
        }
        XCTAssertEqual(notUploaded, .failedPrecondition)

        let content = try XCTUnwrap(AttachmentRules.content([attachment], caption: "look"))
        let sent = try await keys(alice).send(
            content: content, to: id, members: members, clientMessageID: "a1",
            blobIDs: [attachment.blobID], alice)
        XCTAssertEqual(sent.blobIds, [attachment.blobID])
        let resent = try await keys(alice).send(
            content: content, to: id, members: members, clientMessageID: "a1",
            blobIDs: [attachment.blobID], alice)
        XCTAssertEqual(resent.seq, sent.seq)

        try await receives(bob, sent, attachment, prepared.data)

        try await alice.deleteMessage(conversationID: id, seq: sent.seq)
        _ = try await next(bob.events) { $0.messageDeleted.seq == sent.seq }
        let history = try await bob.getMessages(conversationID: id, beforeSeq: 0, limit: 10)
        XCTAssertEqual(history.map(\.seq), [sent.seq])
        XCTAssertTrue(history.first?.blobIds.isEmpty == true)
        await alice.close()
        await bob.close()
    }

    /// The peer gets the message with its blob id, opens it to the attachment, and downloads
    /// the blob: ciphertext with the digest sent, which decrypts to the file.
    private func receives(
        _ bob: Connection, _ sent: Wrinkle_V1_Message, _ attachment: Wrinkle_V1_Attachment,
        _ data: Data
    ) async throws {
        let base = try XCTUnwrap(BlobAddress.base(gateway: url))
        let got = try await next(bob.events) { $0.message.message.seq == sent.seq }
        XCTAssertEqual(got.message.message.blobIds, [attachment.blobID])
        let bobKeys = try keys(bob)
        try await bobKeys.fetchKeys(sent.conversationID, bob, after: 0)
        let opened = try XCTUnwrap(bobKeys.open(got.message.message))
        XCTAssertEqual(opened.preview, "look")
        let received = try XCTUnwrap(opened.attachments.items.first)
        XCTAssertEqual(received, attachment)

        let bobFiles = files()
        let blob = try await Blobs(directory: FileManager.default.temporaryDirectory)
            .fetch(blob: received.blobID, base: base)
        XCTAssertNotEqual(blob, data)
        XCTAssertEqual(E2EE.digest(blob), received.digest)
        let plain = try await bobFiles.data(received, base: base)
        XCTAssertEqual(plain, data)
        let file = try await bobFiles.file(received, base: base)
        XCTAssertEqual(file.lastPathComponent, "view.jpg")
        XCTAssertEqual(try Data(contentsOf: file), data)
    }
}
