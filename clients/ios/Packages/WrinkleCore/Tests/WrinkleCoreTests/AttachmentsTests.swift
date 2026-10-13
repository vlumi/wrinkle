import XCTest

@testable import WrinkleCore

@MainActor
final class AttachmentsTests: XCTestCase {
    private let conv7 = Data(repeating: 7, count: 16)
    private let me = Data(repeating: 1, count: 16)
    private let bob = Data(repeating: 2, count: 16)

    private func item(_ n: UInt8, image: Bool = true, name: String = "a.jpg")
        -> Wrinkle_V1_Attachment
    {
        .with {
            $0.blobID = Data(repeating: n, count: 16)
            $0.key = Data(repeating: n, count: 32)
            $0.digest = Data(repeating: n, count: 32)
            $0.mediaType = image ? "image/jpeg" : "application/pdf"
            $0.size = 1000
            $0.name = name
            if image {
                $0.width = 400
                $0.height = 300
            }
        }
    }

    /// The shared vector (clients/go/e2ee/testdata/vectors.json, "blob").
    func testBlobVector() throws {
        let key = try XCTUnwrap(
            Data(hex: "606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f"))
        let nonce = try XCTUnwrap(Data(hex: "707172737475767778797a7b"))
        let file = Data(0..<40)
        XCTAssertEqual(E2EE.blobAAD.hex, "7772696e6b6c6520626c6f62207631")
        let blob = try E2EE.sealBlob(file, key: key, nonce: nonce)
        XCTAssertEqual(
            blob.hex,
            "707172737475767778797a7b19fb13fc676977dc5afe2d019e36fe7504bbf65a2a23b9348affc6f9d572"
                + "e3b949e9edfb8bf0f3a39d7cca5f7501b448f8adbaa294c33031")
        let digest = try XCTUnwrap(
            Data(hex: "3a7bc0ed87733b5b648622f83b739c8e64d7efd10182dbd25f2c87f036235373"))
        XCTAssertEqual(E2EE.digest(blob), digest)
        XCTAssertEqual(try E2EE.decryptBlob(blob, key: key, digest: digest), file)

        // A changed blob fails its digest; a wrong key fails to decrypt.
        var changed = blob
        changed[20] ^= 1
        XCTAssertThrowsError(try E2EE.decryptBlob(changed, key: key, digest: digest))
        XCTAssertThrowsError(
            try E2EE.decryptBlob(changed, key: key, digest: E2EE.digest(changed)))
        XCTAssertThrowsError(
            try E2EE.decryptBlob(blob, key: Data(repeating: 0, count: 32), digest: digest))

        let fresh = try E2EE.encryptBlob(file)
        XCTAssertEqual(fresh.key.count, 32)
        XCTAssertEqual(fresh.blob.count, file.count + 28)
        XCTAssertEqual(
            try E2EE.decryptBlob(fresh.blob, key: fresh.key, digest: fresh.digest), file)
    }

    func testAttachmentPushBody() throws {
        var m = Wrinkle_V1_Message.with {
            $0.conversationID = conv7
            $0.senderUserID = bob
            $0.clientMessageID = "p1"
        }
        m.seal(content: .with { $0.attachments = .with { $0.items = [item(1), item(2)] } })
        let info: [AnyHashable: Any] = [
            "conversation_id": conv7.hex, "sender_user_id": bob.hex, "client_message_id": "p1",
            "epoch": NSNumber(value: TestSealing.epoch),
            "nonce": m.sealed.nonce.base64EncodedString(),
            "ciphertext": m.sealed.ciphertext.base64EncodedString(),
        ]
        let keys = TestSealing.keys(conv7)
        XCTAssertEqual(PushContent.body(info, keys: keys, names: SharedNames()), "2 photos")
        let names = SharedNames(groups: [conv7.hex], names: [bob.hex: "bob"])
        XCTAssertEqual(PushContent.body(info, keys: keys, names: names), "bob: 2 photos")
    }

    func testPreview() {
        func preview(_ items: [Wrinkle_V1_Attachment], caption: String = "") -> String {
            Wrinkle_V1_Content.with {
                $0.attachments = .with {
                    $0.items = items
                    $0.caption = caption
                }
            }.preview
        }
        XCTAssertEqual(preview([item(1)]), "Photo")
        XCTAssertEqual(preview([item(1), item(2), item(3)]), "3 photos")
        XCTAssertEqual(preview([item(1, image: false, name: "report.pdf")]), "File: report.pdf")
        XCTAssertEqual(preview([item(1), item(2, image: false)]), "2 files")
        XCTAssertEqual(preview([item(1)], caption: "look"), "look")
    }

    func testAttachmentMessageOpens() throws {
        let store = try Store.inMemory(keys: TestSealing.keys(conv7))
        _ = store.upsert(
            .with {
                $0.conversationID = conv7
                $0.peer.userID = bob
            })
        let items = [item(1), item(2, image: false, name: "notes.txt")]
        let event = Wrinkle_V1_Event.with {
            $0.cursor = 1
            $0.message.message = .with {
                $0.conversationID = conv7
                $0.seq = 1
                $0.senderUserID = bob
                $0.clientMessageID = "a1"
                $0.sentAt = .init(date: Date())
                $0.blobIds = items.map(\.blobID)
                $0.seal(
                    content: .with {
                        $0.attachments = .with {
                            $0.items = items
                            $0.caption = "both"
                        }
                    })
            }
        }
        XCTAssertEqual(store.apply(event, me: me.hex)?.lastText, "both")
        let m = try XCTUnwrap(store.message(1, in: conv7.hex))
        XCTAssertEqual(m.text, "both")
        XCTAssertEqual(m.attachmentContent?.items, items)
        XCTAssertTrue(m.deletable(by: bob.hex))

        store.markDeleted(1, in: conv7.hex, at: Date())
        XCTAssertNil(m.attachments)
        XCTAssertEqual(m.text, "")
    }

    func testMalformedAttachmentsAreNotShown() {
        var short = item(1)
        short.key = Data(count: 3)
        XCTAssertFalse(AttachmentRules.valid(.with { $0.items = [short] }))
        XCTAssertFalse(AttachmentRules.valid(Wrinkle_V1_Attachments()))
        XCTAssertFalse(
            AttachmentRules.valid(.with { $0.items = (0..<11).map { item(UInt8($0)) } }))
        XCTAssertTrue(AttachmentRules.valid(.with { $0.items = [item(1)] }))
    }

    func testThumbnailBudgetFitsTenAndACaption() throws {
        XCTAssertEqual(AttachmentRules.thumbnailBudget(count: 1, caption: "", names: ["a"]), 16384)
        let caption = String(repeating: "x", count: AttachmentRules.maxCaption)
        let names = (0..<10).map { "photo-\($0).jpg" }
        let budget = AttachmentRules.thumbnailBudget(count: 10, caption: caption, names: names)
        XCTAssertLessThan(budget, 5000)
        let items = (0..<10).map { n in
            var a = item(UInt8(n), name: names[n])
            a.thumbnail = Data(repeating: 0xff, count: budget)
            return a
        }
        let content = try XCTUnwrap(AttachmentRules.content(items, caption: caption))
        XCTAssertEqual(content.attachments.items.filter { $0.thumbnail.isEmpty }.count, 0)
        XCTAssertLessThanOrEqual(
            try (content.serializedBytes() as Data).count, AttachmentRules.maxContentBytes)
    }

    func testContentDropsThumbnailsToFit() throws {
        let items = (0..<10).map { n in
            var a = item(UInt8(n))
            a.thumbnail = Data(repeating: 0xff, count: AttachmentRules.maxThumbnailBytes)
            return a
        }
        let content = try XCTUnwrap(AttachmentRules.content(items, caption: ""))
        let kept = content.attachments.items.filter { !$0.thumbnail.isEmpty }.count
        XCTAssertGreaterThan(kept, 0)
        XCTAssertLessThan(kept, 10)
        XCTAssertLessThanOrEqual(
            try (content.serializedBytes() as Data).count, AttachmentRules.maxContentBytes)

        let huge = [item(1, name: String(repeating: "n", count: 60000))]
        XCTAssertNil(AttachmentRules.content(huge, caption: ""))
    }

    func testPreparesPictures() throws {
        let png = try testPNG(width: 3000, height: 1500)
        let file = OutgoingFile(data: png, name: "wide.png", mediaType: "image/png")
        let p = AttachmentRules.prepare(file, thumbnailBytes: 4096)
        XCTAssertEqual(p.mediaType, "image/jpeg")
        XCTAssertEqual(p.name, "wide.jpg")
        XCTAssertEqual(Array(p.data.prefix(2)), [0xff, 0xd8])
        XCTAssertEqual(p.width, 2048)
        XCTAssertEqual(p.height, 1024)
        XCTAssertFalse(p.thumbnail.isEmpty)
        XCTAssertLessThanOrEqual(p.thumbnail.count, 4096)
        let thumb = try XCTUnwrap(AttachmentImage.decode(p.thumbnail, maxSide: 1000))
        XCTAssertEqual(max(thumb.width, thumb.height), AttachmentImage.thumbnailSide)

        // Noise needs a smaller thumbnail to fit a small budget.
        let noisy = OutgoingFile(
            data: try testPNG(width: 800, height: 800, noise: true), name: "n.png",
            mediaType: "image/png")
        let n = AttachmentRules.prepare(noisy, thumbnailBytes: 2048)
        XCTAssertEqual(n.width, 800)
        XCTAssertLessThanOrEqual(n.thumbnail.count, 2048)

        // Small pictures are not enlarged; other files go as they are.
        let small = AttachmentRules.prepare(
            OutgoingFile(
                data: try testPNG(width: 100, height: 50), name: "s", mediaType: "image/png"),
            thumbnailBytes: 4096)
        XCTAssertEqual(small.width, 100)
        XCTAssertEqual(small.name, "s.jpg")
        let text = OutgoingFile(data: Data("hello".utf8), name: "a.txt", mediaType: "text/plain")
        let t = AttachmentRules.prepare(text, thumbnailBytes: 4096)
        XCTAssertEqual(t.data, text.data)
        XCTAssertEqual(t.mediaType, "text/plain")
        XCTAssertTrue(t.thumbnail.isEmpty)
        XCTAssertEqual(t.width, 0)
        // Something claiming to be a picture that is not one goes as it is.
        let bogus = OutgoingFile(data: Data("no".utf8), name: "x.png", mediaType: "image/png")
        XCTAssertEqual(AttachmentRules.prepare(bogus, thumbnailBytes: 4096).name, "x.png")
        XCTAssertEqual(OutgoingFile.mediaType(forName: "a.pdf"), "application/pdf")
        XCTAssertEqual(OutgoingFile.mediaType(forName: "noext"), "application/octet-stream")
    }

    func testSafeNames() {
        XCTAssertEqual(AttachmentFiles.safeName("../etc/passwd"), "_etc_passwd")
        XCTAssertEqual(AttachmentFiles.safeName(""), "file")
        XCTAssertEqual(AttachmentFiles.safeName(".hidden"), "hidden")
        XCTAssertEqual(AttachmentFiles.safeName("report.pdf"), "report.pdf")
    }
}
