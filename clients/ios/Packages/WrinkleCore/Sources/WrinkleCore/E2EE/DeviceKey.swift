import CryptoKit
import Foundation

/// This device's X25519 key pair; the private key never leaves the vault.
public enum DeviceKey {
    private static func account(_ deviceID: UInt64) -> String { "device.\(deviceID)" }

    public static func load(deviceID: UInt64, from vault: Vault)
        -> Curve25519.KeyAgreement.PrivateKey?
    {
        vault.get(account(deviceID)).flatMap {
            try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: $0)
        }
    }

    /// The stored key, or a new one, stored. The flag tells whether it is new.
    public static func loadOrCreate(deviceID: UInt64, in vault: Vault)
        -> (key: Curve25519.KeyAgreement.PrivateKey, new: Bool)
    {
        if let key = load(deviceID: deviceID, from: vault) { return (key, false) }
        let key = Curve25519.KeyAgreement.PrivateKey()
        vault.set(account(deviceID), key.rawRepresentation)
        return (key, true)
    }

    public static func forget(deviceID: UInt64, in vault: Vault) {
        vault.delete(account(deviceID))
    }
}
