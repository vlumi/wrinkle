import CryptoKit
import XCTest

@testable import WrinkleCore

/// The shared test vectors (clients/go/e2ee/testdata/vectors.json): every client's
/// cryptography must produce these bytes.
final class E2EETests: XCTestCase {
    private func hex(_ s: String) -> Data { Data(hex: s)! }

    private func privateKey(_ s: String) throws -> Curve25519.KeyAgreement.PrivateKey {
        try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: hex(s))
    }

    private struct Device {
        let userID: String
        let deviceID: UInt64
        let privateKey: String
        let publicKey: String
    }

    private let a1 = Device(
        userID: "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf", deviceID: 9_223_372_036_854_775_809,
        privateKey: "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
        publicKey: "07a37cbc142093c8b755dc1b10e86cb426374ad16aa853ed0bdfc0b2b86d1c7c")
    private let a2 = Device(
        userID: "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf", deviceID: 81_985_529_216_486_895,
        privateKey: "2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40",
        publicKey: "5869aff450549732cbaaed5e5df9b30a6da31cb0e5742bad5ad4a1a768f1a67b")
    private let b1 = Device(
        userID: "b0b1b2b3b4b5b6b7b8b9babbbcbdbebf", deviceID: 18_364_758_544_493_064_720,
        privateKey: "4142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f60",
        publicKey: "64b101b1d0be5a8704bd078f9895001fc03e8e9f9522f188dd128d9846d48466")

    private let conversation = "c0c1c2c3c4c5c6c7c8c9cacbcccdcecf"
    private let epochKey = "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f"

    func testRFC7748() throws {
        let alice = try privateKey(
            "77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
        let bob = try privateKey("5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb")
        XCTAssertEqual(
            alice.publicKey.rawRepresentation.hex,
            "8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")
        XCTAssertEqual(
            bob.publicKey.rawRepresentation.hex,
            "de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f")
        let shared = "4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742"
        XCTAssertEqual(try E2EE.sharedSecret(alice, bob.publicKey.rawRepresentation).hex, shared)
        XCTAssertEqual(try E2EE.sharedSecret(bob, alice.publicKey.rawRepresentation).hex, shared)
    }

    func testRFC5869() {
        let okm = HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(
                data: hex("0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b")),
            salt: hex("000102030405060708090a0b0c"), info: hex("f0f1f2f3f4f5f6f7f8f9"),
            outputByteCount: 42)
        XCTAssertEqual(
            okm.withUnsafeBytes { Data($0) }.hex,
            "3cb25f25faacd57a90434f64d0362f2a2d2d0a90cf1a5a4c5db02d56ecc4c5bf34007208d5b887185865")
    }

    func testDeviceKeys() throws {
        for d in [a1, a2, b1] {
            XCTAssertEqual(
                try privateKey(d.privateKey).publicKey.rawRepresentation.hex, d.publicKey)
        }
    }

    private var wrapContext: E2EE.WrapContext {
        E2EE.WrapContext(
            conversationID: hex(conversation), epoch: 1, senderUserID: hex(a1.userID),
            senderDeviceID: a1.deviceID, recipientUserID: hex(b1.userID),
            recipientDeviceID: b1.deviceID)
    }

    func testWrap() throws {
        let sender = try privateKey(a1.privateKey)
        let recipient = try privateKey(b1.privateKey)
        let context = wrapContext
        let secret = try E2EE.sharedSecret(sender, hex(b1.publicKey))
        XCTAssertEqual(
            secret.hex, "26c2c17fdb82161cb21ad16e721315355b64d1763119b10bfc962530dc7cc163")
        XCTAssertEqual(
            context.info.hex,
            "7772696e6b6c652077726170207631c0c1c2c3c4c5c6c7c8c9cacbcccdcecf00000001"
                + "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf8000000000000001"
                + "b0b1b2b3b4b5b6b7b8b9babbbcbdbebffedcba9876543210")
        XCTAssertEqual(
            E2EE.hkdf(secret, info: context.info).withUnsafeBytes { Data($0) }.hex,
            "ecf693d772f86f77a207dace9faf5549d85f6dffa478399ae033420b0698ee05")
        let wrapped = try E2EE.wrap(
            hex(epochKey), sender: sender, recipientPublic: hex(b1.publicKey), context: context,
            nonce: hex("101112131415161718191a1b"))
        XCTAssertEqual(
            wrapped.hex,
            "101112131415161718191a1be91061dfe93edfe08a2cde959f649d2ffa77902986e697f3f900b36a"
                + "477dc26a8f89b1c261700531425d918ccf7a73ac")
        XCTAssertEqual(wrapped.count, E2EE.wrappedLength)

        let unwrapped = try E2EE.unwrap(
            wrapped, recipient: recipient, senderPublic: hex(a1.publicKey), context: context)
        XCTAssertEqual(unwrapped.hex, epochKey)
        // Unwrapped as if for another device, or claiming another sender, it fails.
        var other = context
        other.recipientDeviceID += 1
        XCTAssertThrowsError(
            try E2EE.unwrap(
                wrapped, recipient: recipient, senderPublic: hex(a1.publicKey), context: other))
        XCTAssertThrowsError(
            try E2EE.unwrap(
                wrapped, recipient: recipient, senderPublic: hex(a2.publicKey), context: context))
    }

    private var contentContext: E2EE.ContentContext {
        E2EE.ContentContext(
            conversationID: hex(conversation), epoch: 1, senderUserID: hex(a1.userID),
            clientMessageID: "client-message-1")
    }

    func testSeal() throws {
        let context = contentContext
        let content = Wrinkle_V1_Content.with { $0.text = "hello" }
        XCTAssertEqual(try (content.serializedBytes() as Data).hex, "0a0568656c6c6f")
        XCTAssertEqual(
            context.aad.hex,
            "7772696e6b6c6520636f6e74656e74207631c0c1c2c3c4c5c6c7c8c9cacbcccdcecf00000001"
                + "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf636c69656e742d6d6573736167652d31")
        let sealed = try E2EE.seal(
            content, key: hex(epochKey), context: context, nonce: hex("303132333435363738393a3b"))
        XCTAssertEqual(sealed.epoch, 1)
        XCTAssertEqual(sealed.nonce.hex, "303132333435363738393a3b")
        XCTAssertEqual(sealed.ciphertext.hex, "1fff712ff7e3e70fa783c1fc72407a404036b0a88eaf15")

        XCTAssertEqual(try E2EE.open(sealed, key: hex(epochKey), context: context).text, "hello")
        var moved = context
        moved.clientMessageID = "client-message-2"
        XCTAssertThrowsError(try E2EE.open(sealed, key: hex(epochKey), context: moved))
        var otherSender = context
        otherSender.senderUserID = hex(b1.userID)
        XCTAssertThrowsError(try E2EE.open(sealed, key: hex(epochKey), context: otherSender))
    }

    func testFingerprintsAndSafetyNumber() {
        let alice = E2EE.fingerprint(
            userID: hex(a1.userID),
            devices: [a1.deviceID: hex(a1.publicKey), a2.deviceID: hex(a2.publicKey)])
        let bob = E2EE.fingerprint(
            userID: hex(b1.userID), devices: [b1.deviceID: hex(b1.publicKey)])
        XCTAssertEqual(
            alice.hex, "83f8d669a171e66fe9f42fa06af663015c895d9e6b63b6a577514567ca391efc")
        XCTAssertEqual(bob.hex, "b091119e7a6e849daf44d9740e1f4b585658facb81c69611fa0290457d0b4ff5")
        XCTAssertEqual(E2EE.safetyNumber(alice, bob), "50618 81616 18083 62639 81203 54787")
        XCTAssertEqual(E2EE.safetyNumber(bob, alice), "50618 81616 18083 62639 81203 54787")
    }

    func testEpochKeysStore() {
        let vault = MemoryVault()
        let keys = EpochKeys(vault: vault)
        let c = hex(conversation)
        XCTAssertEqual(keys.highest(c), 0)
        keys.put(c, epoch: 2, key: Data(repeating: 2, count: 32))
        keys.put(c, epoch: 1, key: Data(repeating: 1, count: 32))
        // Another instance over the same vault, as the extension has, reads them.
        let reader = EpochKeys(vault: vault)
        XCTAssertEqual(reader.highest(c), 2)
        XCTAssertEqual(reader.key(c, epoch: 1), Data(repeating: 1, count: 32))
        XCTAssertNil(reader.key(c, epoch: 3))
        XCTAssertEqual(reader.conversations(), [c])
        vault.set("names", Data("{}".utf8))
        keys.forgetAll()
        XCTAssertTrue(reader.conversations().isEmpty)
        XCTAssertNotNil(vault.get("names"))
    }

    func testDeviceKeyIsKept() {
        let vault = MemoryVault()
        let first = DeviceKey.loadOrCreate(deviceID: 7, in: vault)
        XCTAssertTrue(first.new)
        let again = DeviceKey.loadOrCreate(deviceID: 7, in: vault)
        XCTAssertFalse(again.new)
        XCTAssertEqual(again.key.rawRepresentation, first.key.rawRepresentation)
        DeviceKey.forget(deviceID: 7, in: vault)
        XCTAssertNil(DeviceKey.load(deviceID: 7, from: vault))
    }

    /// A push as the backend sends it for the sealed vector message.
    private func push(epoch: UInt32 = 1) throws -> [AnyHashable: Any] {
        let sealed = try E2EE.seal(
            .with { $0.text = "hello" }, key: hex(epochKey), context: contentContext,
            nonce: hex("303132333435363738393a3b"))
        return [
            "conversation_id": conversation, "seq": NSNumber(value: 3),
            "epoch": NSNumber(value: epoch), "nonce": sealed.nonce.base64EncodedString(),
            "ciphertext": sealed.ciphertext.base64EncodedString(),
            "sender_user_id": a1.userID, "client_message_id": "client-message-1",
        ]
    }

    func testPushBody() throws {
        let keys = EpochKeys(vault: MemoryVault())
        let info = try push()
        XCTAssertNil(PushContent.body(info, keys: keys, names: SharedNames()))
        keys.put(hex(conversation), epoch: 1, key: hex(epochKey))
        XCTAssertEqual(PushContent.body(info, keys: keys, names: SharedNames()), "hello")
        let names = SharedNames(groups: [conversation], names: [a1.userID: "alice"])
        XCTAssertEqual(PushContent.body(info, keys: keys, names: names), "alice: hello")
        // Too large a payload leaves the sealed fields out.
        XCTAssertNil(
            PushContent.body(["conversation_id": conversation], keys: keys, names: names))
        XCTAssertNil(PushContent.body(try push(epoch: 2), keys: keys, names: names))
    }

    func testSharedNamesRoundTrip() {
        let vault = MemoryVault()
        XCTAssertEqual(SharedNames.load(from: vault), SharedNames())
        let names = SharedNames(groups: ["g"], names: ["u": "una"])
        names.save(to: vault)
        XCTAssertEqual(SharedNames.load(from: vault), names)
        SharedNames.forget(in: vault)
        XCTAssertEqual(SharedNames.load(from: vault), SharedNames())
    }

    func testVerificationState() {
        let a = Data([1])
        let b = Data([2])
        XCTAssertEqual(VerificationState(verified: nil, current: a), .unverified)
        XCTAssertEqual(VerificationState(verified: a, current: nil), .verified)
        XCTAssertEqual(VerificationState(verified: a, current: a), .verified)
        XCTAssertEqual(VerificationState(verified: a, current: b), .changed)
    }
}
