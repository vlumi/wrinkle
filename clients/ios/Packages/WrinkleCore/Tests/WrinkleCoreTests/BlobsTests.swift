import CoreGraphics
import XCTest

@testable import WrinkleCore

final class BlobsTests: XCTestCase {
    private func base(_ gateway: String) -> String? {
        BlobAddress.base(gateway: URL(string: gateway)!)?.absoluteString
    }

    func testBlobAddressFromGateway() {
        XCTAssertEqual(base("wss://mac.local:8443/v1/ws"), "https://mac.local:8443")
        XCTAssertEqual(base("wss://example.org/v1/ws"), "https://example.org")
        XCTAssertEqual(base("WSS://Mac.local:8443/v1/ws?x=1"), "https://Mac.local:8443")
        XCTAssertEqual(base("ws://localhost:8080/v1/ws"), "http://localhost:9200")
        XCTAssertEqual(base("ws://127.0.0.1:8080/v1/ws"), "http://127.0.0.1:9200")
        XCTAssertEqual(base("ws://[::1]:8080/v1/ws"), "http://[::1]:9200")
        XCTAssertNil(base("http://localhost:8080/"))
        XCTAssertNil(base("ws:///v1/ws"))
    }

    func testBlobURLs() {
        let base = URL(string: "http://localhost:9200")!
        let id = Data([0x00, 0x11, 0xab] + Array(repeating: 0xff, count: 13))
        XCTAssertEqual(
            BlobAddress.url(blob: id, base: base).absoluteString,
            "http://localhost:9200/blobs/0011abffffffffffffffffffffffffff")
        let path = "/blobs/00?X-Amz-Credential=GK%2F20260102&X-Amz-Signature=41f9"
        XCTAssertEqual(
            BlobAddress.url(uploadPath: path, base: base)?.absoluteString,
            "http://localhost:9200" + path)
        XCTAssertNil(BlobAddress.url(uploadPath: "/elsewhere", base: base))
    }

    func testAvatarSizing() {
        XCTAssertEqual(
            AvatarImage.cropRect(width: 1000, height: 600),
            CGRect(x: 200, y: 0, width: 600, height: 600))
        XCTAssertEqual(
            AvatarImage.cropRect(width: 300, height: 401),
            CGRect(x: 0, y: 50, width: 300, height: 300))
        XCTAssertEqual(AvatarImage.outputSide(for: 600), 512)
        XCTAssertEqual(AvatarImage.outputSide(for: 300), 300)
        XCTAssertEqual(AvatarImage.outputSide(for: 0), 1)
        // A 4000 × 3000 photo decodes with its shorter side at 512 or just above.
        XCTAssertEqual(AvatarImage.decodeSize(longer: 4000, shorter: 3000), 683)
        XCTAssertEqual(AvatarImage.decodeSize(longer: 400, shorter: 300), 400)
    }

    private func size(of jpeg: Data) throws -> (Int, Int) {
        let image = try XCTUnwrap(AvatarImage.decode(jpeg))
        return (image.width, image.height)
    }

    func testAvatarJPEG() throws {
        let large = try XCTUnwrap(AvatarImage.jpeg(from: testPNG(width: 1600, height: 900)))
        XCTAssertEqual(Array(large.prefix(2)), [0xff, 0xd8])
        let (w, h) = try size(of: large)
        XCTAssertEqual(w, 512)
        XCTAssertEqual(h, 512)

        let small = try XCTUnwrap(AvatarImage.jpeg(from: testPNG(width: 200, height: 300)))
        let (sw, sh) = try size(of: small)
        XCTAssertEqual(sw, 200)
        XCTAssertEqual(sh, 200)

        let noisy = try XCTUnwrap(
            AvatarImage.jpeg(from: testPNG(width: 2000, height: 2000, noise: true)))
        XCTAssertLessThanOrEqual(noisy.count, AvatarImage.maxBytes)

        XCTAssertNil(AvatarImage.jpeg(from: Data("not a picture".utf8)))
    }

    func testBlobCacheServesFromDisk() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(
            path: "blobs-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let id = Data(repeating: 0xab, count: 16)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try Data("cached".utf8).write(to: dir.appending(path: id.hex))
        // Nothing listens on the discard port: the bytes can only come from the cache.
        let blobs = Blobs(directory: dir)
        let data = try await blobs.data(blob: id, base: URL(string: "http://127.0.0.1:9")!)
        XCTAssertEqual(data, Data("cached".utf8))
    }
}
