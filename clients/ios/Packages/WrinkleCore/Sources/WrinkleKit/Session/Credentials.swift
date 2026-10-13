import Foundation
import WrinkleCore

/// What the device keeps to connect again without the password, in the Keychain.
struct Credentials: Codable, Equatable {
    var userID: Data
    var deviceID: UInt64
    var deviceToken: String
    var serverURL: String

    private static let account = "credentials"

    /// The app's own Keychain, readable in the background after the first unlock for
    /// reconnecting.
    private static var vault: Vault { KeychainVault(service: Vaults.identity, accessGroup: nil) }

    /// The saved credentials, or nil when signed out.
    static func load() -> Credentials? {
        vault.get(account).flatMap { try? JSONDecoder().decode(Credentials.self, from: $0) }
    }

    func save() throws(VaultError) {
        guard let data = try? JSONEncoder().encode(self) else { return }
        try Self.vault.set(Self.account, data)
    }

    static func delete() {
        vault.delete(account)
    }
}
