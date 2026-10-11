import CryptoKit
import Foundation

/// The cryptography of content.proto: wrapping epoch keys for devices, sealing content,
/// fingerprints and safety numbers. Nonces are parameters so the test vectors can fix them;
/// callers pass random ones.
public enum E2EE {
    public static let keyLength = 32
    public static let nonceLength = 12
    public static let wrappedLength = 60

    public struct Failure: Error, Equatable {
        public let reason: String
        init(_ reason: String) { self.reason = reason }
    }

    public static func randomNonce() -> Data { random(nonceLength) }

    public static func newEpochKey() -> Data { random(keyLength) }

    private static func random(_ n: Int) -> Data {
        SymmetricKey(size: SymmetricKeySize(bitCount: n * 8)).withUnsafeBytes { Data($0) }
    }

    /// X25519(own, other), 32 bytes.
    public static func sharedSecret(
        _ own: Curve25519.KeyAgreement.PrivateKey, _ otherPublic: Data
    ) throws -> Data {
        let other = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: otherPublic)
        return try own.sharedSecretFromKeyAgreement(with: other).withUnsafeBytes { Data($0) }
    }

    /// HKDF-SHA256 with a salt of 32 zero bytes, 32 bytes out.
    public static func hkdf(_ ikm: Data, info: Data) -> SymmetricKey {
        HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: ikm), salt: Data(count: 32), info: info,
            outputByteCount: keyLength)
    }

    /// Who wraps an epoch key for whom, in which conversation and epoch.
    public struct WrapContext: Sendable {
        public var conversationID: Data
        public var epoch: UInt32
        public var senderUserID: Data
        public var senderDeviceID: UInt64
        public var recipientUserID: Data
        public var recipientDeviceID: UInt64

        public init(
            conversationID: Data, epoch: UInt32, senderUserID: Data, senderDeviceID: UInt64,
            recipientUserID: Data, recipientDeviceID: UInt64
        ) {
            self.conversationID = conversationID
            self.epoch = epoch
            self.senderUserID = senderUserID
            self.senderDeviceID = senderDeviceID
            self.recipientUserID = recipientUserID
            self.recipientDeviceID = recipientDeviceID
        }

        public var info: Data {
            var d = Data("wrinkle wrap v1".utf8)
            d.append(conversationID)
            d.append(bigEndian: epoch)
            d.append(senderUserID)
            d.append(bigEndian: senderDeviceID)
            d.append(recipientUserID)
            d.append(bigEndian: recipientDeviceID)
            return d
        }
    }

    /// The epoch key wrapped by the sender for the recipient: nonce, ciphertext and tag.
    public static func wrap(
        _ key: Data, sender: Curve25519.KeyAgreement.PrivateKey, recipientPublic: Data,
        context: WrapContext, nonce: Data
    ) throws -> Data {
        let wk = hkdf(try sharedSecret(sender, recipientPublic), info: context.info)
        let box = try AES.GCM.seal(key, using: wk, nonce: try AES.GCM.Nonce(data: nonce))
        return nonce + box.ciphertext + box.tag
    }

    public static func unwrap(
        _ wrapped: Data, recipient: Curve25519.KeyAgreement.PrivateKey, senderPublic: Data,
        context: WrapContext
    ) throws -> Data {
        guard wrapped.count == wrappedLength else { throw Failure("wrapped key length") }
        let wk = hkdf(try sharedSecret(recipient, senderPublic), info: context.info)
        let key = try open(wrapped, key: wk, aad: Data())
        guard key.count == keyLength else { throw Failure("epoch key length") }
        return key
    }

    /// What binds sealed content to its message.
    public struct ContentContext: Sendable {
        public var conversationID: Data
        public var epoch: UInt32
        public var senderUserID: Data
        public var clientMessageID: String

        public init(
            conversationID: Data, epoch: UInt32, senderUserID: Data, clientMessageID: String
        ) {
            self.conversationID = conversationID
            self.epoch = epoch
            self.senderUserID = senderUserID
            self.clientMessageID = clientMessageID
        }

        /// The context of a message as the server delivers it.
        public init(_ m: Wrinkle_V1_Message) {
            self.init(
                conversationID: m.conversationID, epoch: m.sealed.epoch,
                senderUserID: m.senderUserID, clientMessageID: m.clientMessageID)
        }

        public var aad: Data {
            var d = Data("wrinkle content v1".utf8)
            d.append(conversationID)
            d.append(bigEndian: epoch)
            d.append(senderUserID)
            d.append(Data(clientMessageID.utf8))
            return d
        }
    }

    public static func seal(
        _ content: Wrinkle_V1_Content, key: Data, context: ContentContext, nonce: Data
    ) throws -> Wrinkle_V1_Sealed {
        let box = try AES.GCM.seal(
            try content.serializedBytes() as Data, using: SymmetricKey(data: key),
            nonce: try AES.GCM.Nonce(data: nonce), authenticating: context.aad)
        return .with {
            $0.epoch = context.epoch
            $0.nonce = nonce
            $0.ciphertext = box.ciphertext + box.tag
        }
    }

    public static func open(
        _ sealed: Wrinkle_V1_Sealed, key: Data, context: ContentContext
    ) throws -> Wrinkle_V1_Content {
        let plain = try open(
            sealed.nonce + sealed.ciphertext, key: SymmetricKey(data: key), aad: context.aad)
        return try Wrinkle_V1_Content(serializedBytes: plain)
    }

    /// Opens nonce, ciphertext and tag.
    private static func open(_ combined: Data, key: SymmetricKey, aad: Data) throws -> Data {
        guard combined.count >= nonceLength + 16 else { throw Failure("ciphertext too short") }
        let box = try AES.GCM.SealedBox(combined: combined)
        return try AES.GCM.open(box, using: key, authenticating: aad)
    }

    /// A user's fingerprint over their devices' keys, in ascending device id order.
    public static func fingerprint(userID: Data, devices: [UInt64: Data]) -> Data {
        var d = Data("wrinkle fingerprint v1".utf8)
        d.append(userID)
        for id in devices.keys.sorted() {
            d.append(bigEndian: id)
            d.append(devices[id] ?? Data())
        }
        return Data(SHA256.hash(data: d))
    }

    /// Six groups of five digits, the same for both users.
    public static func safetyNumber(_ a: Data, _ b: Data) -> String {
        let (lo, hi) = a.lexicographicallyPrecedes(b) ? (a, b) : (b, a)
        let h = Array(SHA256.hash(data: lo + hi))
        return (0..<6).map { i in
            let n = h[i * 5..<i * 5 + 5].reduce(UInt64(0)) { $0 << 8 | UInt64($1) }
            let s = String(n % 100_000)
            return String(repeating: "0", count: 5 - s.count) + s
        }
        .joined(separator: " ")
    }
}

extension Data {
    mutating func append<T: FixedWidthInteger>(bigEndian value: T) {
        Swift.withUnsafeBytes(of: value.bigEndian) { append(contentsOf: $0) }
    }
}
