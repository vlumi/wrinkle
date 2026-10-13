import CoreGraphics
import XCTest

@testable import WrinkleCore

@MainActor
final class StickersTests: XCTestCase {
    private let conv7 = Data(repeating: 7, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)
    private let sticker = Data(repeating: 0x5a, count: 16)

    private func stickerEvent(seq: UInt64, id: Data) -> Wrinkle_V1_Event {
        .with {
            $0.cursor = seq
            $0.message.message = .with {
                $0.conversationID = conv7
                $0.seq = seq
                $0.senderUserID = bob
                $0.clientMessageID = "s\(seq)"
                $0.sentAt = .init(date: Date())
                $0.seal(content: .with { $0.sticker = .with { $0.stickerID = id } })
            }
        }
    }

    func testStickerMessageOpens() throws {
        let store = try Store.inMemory(keys: TestSealing.keys(conv7))
        _ = store.upsert(
            .with {
                $0.conversationID = conv7
                $0.peer.userID = bob
            })
        let touched = store.apply(stickerEvent(seq: 1, id: sticker), me: me.hex)
        XCTAssertEqual(touched?.lastText, "Sticker")
        let m = try XCTUnwrap(store.message(1, in: conv7.hex))
        XCTAssertFalse(m.isLocked)
        XCTAssertTrue(m.isSticker)
        XCTAssertEqual(m.stickerID, sticker.hex)
        XCTAssertEqual(m.text, "")

        // A malformed id is no sticker.
        _ = store.apply(stickerEvent(seq: 2, id: Data([1, 2, 3])), me: me.hex)
        XCTAssertFalse(try XCTUnwrap(store.message(2, in: conv7.hex)).isSticker)

        store.markDeleted(1, in: conv7.hex, at: Date())
        XCTAssertFalse(try XCTUnwrap(store.message(1, in: conv7.hex)).isSticker)
    }

    func testLockedStickerOpensWithTheKey() throws {
        var store = try Store.inMemory()
        _ = store.upsert(
            .with {
                $0.conversationID = conv7
                $0.peer.userID = bob
            })
        _ = store.apply(stickerEvent(seq: 1, id: sticker), me: me.hex)
        XCTAssertTrue(try XCTUnwrap(store.message(1, in: conv7.hex)).isLocked)
        store.keys = TestSealing.keys(conv7)
        store.reopen(conv7.hex)
        XCTAssertEqual(try XCTUnwrap(store.message(1, in: conv7.hex)).stickerID, sticker.hex)
        XCTAssertEqual(store.conversation(conv7.hex)?.lastText, "Sticker")
    }

    func testPaletteMove() {
        let ids = (0..<4).map { Data(repeating: UInt8($0), count: 16) }
        // Down: before the sticker now at the destination.
        XCTAssertEqual(Palette.move(ids, from: 0, to: 2)?.sticker, ids[0])
        XCTAssertEqual(Palette.move(ids, from: 0, to: 2)?.before, ids[2])
        // To the end: no before.
        XCTAssertEqual(Palette.move(ids, from: 1, to: 4)?.sticker, ids[1])
        XCTAssertNil(Palette.move(ids, from: 1, to: 4)?.before)
        // Up.
        XCTAssertEqual(Palette.move(ids, from: 3, to: 0)?.before, ids[0])
        // Where it is: nothing to do.
        XCTAssertNil(Palette.move(ids, from: 2, to: 2))
        XCTAssertNil(Palette.move(ids, from: 2, to: 3))
        XCTAssertNil(Palette.move(ids, from: 4, to: 0))
        XCTAssertNil(Palette.move(ids, from: 0, to: 5))
    }

    func testContentPreview() {
        XCTAssertEqual(Wrinkle_V1_Content.with { $0.text = "hi" }.preview, "hi")
        let s = Wrinkle_V1_Content.with { $0.sticker = .with { $0.stickerID = sticker } }
        XCTAssertEqual(s.preview, "Sticker")
        XCTAssertEqual(s.stickerID, sticker)
        XCTAssertNil(Wrinkle_V1_Content.with { $0.text = "hi" }.stickerID)
    }

    private func image(_ png: Data) throws -> CGImage {
        try XCTUnwrap(StickerImage.decode(png))
    }

    func testStickerPNG() throws {
        let large = try XCTUnwrap(StickerImage.png(from: testPNG(width: 1600, height: 900)))
        XCTAssertEqual(Array(large.prefix(4)), [0x89, 0x50, 0x4e, 0x47])
        XCTAssertLessThanOrEqual(large.count, StickerImage.maxBytes)
        let l = try image(large)
        XCTAssertEqual(l.width, 512)
        XCTAssertEqual(l.height, 512)
        XCTAssertNotEqual(l.alphaInfo, .none)

        let small = try image(
            try XCTUnwrap(StickerImage.png(from: testPNG(width: 120, height: 90))))
        XCTAssertEqual(small.width, 90)
        XCTAssertEqual(small.height, 90)

        // Noise does not compress: it fits only smaller.
        let noisy = try XCTUnwrap(
            StickerImage.png(from: testPNG(width: 1000, height: 1000, noise: true)))
        XCTAssertLessThanOrEqual(noisy.count, StickerImage.maxBytes)
        XCTAssertLessThan(try image(noisy).width, 512)

        XCTAssertNil(StickerImage.png(from: Data("not a picture".utf8)))
    }
}
