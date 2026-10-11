import XCTest

@testable import WrinkleCore

/// Stickers against the live backend: one made from a picture lands in its creator's palette,
/// is sent sealed, opens on the peer's device as a sticker, reads publicly as a blob, and the
/// peer saves it to their own palette, told by a palette event.
extension BackendTests {
    private static func palette(_ e: Wrinkle_V1_Event) -> [Data]? {
        if case .palette(let p) = e.body { return p.stickerIds }
        return nil
    }

    /// Uploads a picture as a sticker and makes it one.
    func createSticker(_ c: Connection, from picture: Data) async throws -> Wrinkle_V1_Sticker {
        let base = try XCTUnwrap(BlobAddress.base(gateway: url))
        let png = try XCTUnwrap(StickerImage.png(from: picture))
        let slot = try await c.createUpload(
            purpose: .sticker, mediaType: StickerImage.mediaType, size: png.count)
        let early = await code { try await c.createSticker(blobID: slot.blobID) }
        XCTAssertEqual(early, .failedPrecondition)
        try await Blobs(
            directory: FileManager.default.temporaryDirectory.appending(
                path: "wrinkle-blobs-\(UUID().uuidString)")
        ).upload(
            png, blob: slot.blobID, uploadPath: slot.uploadPath,
            mediaType: StickerImage.mediaType, base: base)
        return try await c.createSticker(blobID: slot.blobID)
    }

    func testStickerRoundTrip() async throws {
        let (alice, a) = try await registered(unique("ios-alice"))
        let (bob, b) = try await registered(unique("ios-bob"))
        let picture = try testPNG(width: 800, height: 600)

        let sticker = try await createSticker(alice, from: picture)
        XCTAssertEqual(sticker.stickerID.count, 16)
        XCTAssertEqual(sticker.creatorUserID, a.user.userID)
        XCTAssertEqual(sticker.mediaType, StickerImage.mediaType)
        let created = try await next(alice.events) { Self.palette($0) != nil }
        XCTAssertEqual(Self.palette(created), [sticker.stickerID])
        let alicePalette = try await alice.getPalette()
        XCTAssertEqual(alicePalette.map(\.stickerID), [sticker.stickerID])

        let conv = try await alice.openConversation(peerName: b.user.name)
        let id = conv.conversationID
        let sent = try await keys(alice).send(
            content: .with { $0.sticker = .with { $0.stickerID = sticker.stickerID } },
            to: id, members: .direct(peer: b.user.userID), clientMessageID: "s1", alice)
        XCTAssertFalse(sent.sealed.ciphertext.contains(sticker.stickerID))
        let got = try await next(bob.events) { $0.message.message.seq == sent.seq }
        let bobKeys = try keys(bob)
        try await bobKeys.fetchKeys(id, bob, after: 0)
        let content = try XCTUnwrap(bobKeys.open(got.message.message))
        XCTAssertEqual(content.stickerID, sticker.stickerID)
        XCTAssertEqual(content.preview, "Sticker")

        // Anyone reads it, into an empty cache.
        let known = try await bob.getStickers(ids: [Data(count: 16), sticker.stickerID])
        XCTAssertEqual(known.map(\.stickerID), [sticker.stickerID])
        let base = try XCTUnwrap(BlobAddress.base(gateway: url))
        let image = try await Blobs(
            directory: FileManager.default.temporaryDirectory.appending(
                path: "wrinkle-blobs-\(UUID().uuidString)")
        ).data(blob: sticker.stickerID, base: base)
        XCTAssertNotNil(StickerImage.decode(image))

        try await savesAndArranges(bob, sticker.stickerID)
        await alice.close()
        await bob.close()
    }

    /// Saves someone's sticker, makes one, moves it to the front and removes the first.
    private func savesAndArranges(_ bob: Connection, _ sticker: Data) async throws {
        try await bob.saveSticker(id: sticker)
        let saved = try await next(bob.events) { Self.palette($0) != nil }
        XCTAssertEqual(Self.palette(saved), [sticker])
        let bobPalette = try await bob.getPalette()
        XCTAssertEqual(bobPalette.map(\.stickerID), [sticker])
        let unknown = await code { try await bob.saveSticker(id: Data(count: 16)) }
        XCTAssertEqual(unknown, .notFound)

        // A second sticker, moved to the front, then the first removed.
        let second = try await createSticker(bob, from: testPNG(width: 300, height: 300))
        _ = try await next(bob.events) { Self.palette($0)?.count == 2 }
        try await bob.moveSticker(id: second.stickerID, before: sticker)
        let moved = try await next(bob.events) {
            Self.palette($0) == [second.stickerID, sticker]
        }
        XCTAssertNotNil(Self.palette(moved))
        try await bob.removeSticker(id: sticker)
        _ = try await next(bob.events) { Self.palette($0) == [second.stickerID] }
        let after = try await bob.getPalette()
        XCTAssertEqual(after.map(\.stickerID), [second.stickerID])
    }
}
