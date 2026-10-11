import CryptoKit
import Foundation

// MARK: Attachment blobs

extension E2EE {
    static let blobAAD = Data("wrinkle blob v1".utf8)

    /// A file encrypted for upload: its key, the blob and the blob's SHA-256.
    public struct SealedBlob: Sendable {
        public let key: Data
        public let blob: Data
        public let digest: Data
    }

    /// nonce ‖ AES-GCM(key, nonce, file, "wrinkle blob v1"), as uploaded.
    public static func sealBlob(_ file: Data, key: Data, nonce: Data) throws -> Data {
        let box = try AES.GCM.seal(
            file, using: SymmetricKey(data: key), nonce: try AES.GCM.Nonce(data: nonce),
            authenticating: blobAAD)
        return nonce + box.ciphertext + box.tag
    }

    /// Encrypts a file under a new random key.
    public static func encryptBlob(_ file: Data) throws -> SealedBlob {
        let key = newEpochKey()
        let blob = try sealBlob(file, key: key, nonce: randomNonce())
        return SealedBlob(key: key, blob: blob, digest: digest(blob))
    }

    public static func digest(_ data: Data) -> Data { Data(SHA256.hash(data: data)) }

    /// Checks a downloaded blob against its digest, then decrypts it with the file's key.
    public static func decryptBlob(_ blob: Data, key: Data, digest expected: Data) throws -> Data {
        guard digest(blob) == expected else { throw Failure("blob digest differs") }
        guard key.count == keyLength, blob.count >= nonceLength + 16 else {
            throw Failure("blob too short")
        }
        let box = try AES.GCM.SealedBox(combined: blob)
        return try AES.GCM.open(box, using: SymmetricKey(data: key), authenticating: blobAAD)
    }
}
