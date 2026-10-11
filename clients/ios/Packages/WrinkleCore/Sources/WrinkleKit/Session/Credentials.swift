import Foundation
import Security

/// What the device keeps to connect again without the password, in the Keychain.
struct Credentials: Codable, Equatable {
    var userID: Data
    var deviceID: UInt64
    var deviceToken: String
    var serverURL: String

    private static let service = "fi.misaki.wrinkle"
    private static let account = "credentials"

    private static var query: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    /// The saved credentials, or nil when signed out.
    static func load() -> Credentials? {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &item) == errSecSuccess,
            let data = item as? Data
        else { return nil }
        return try? JSONDecoder().decode(Credentials.self, from: data)
    }

    func save() {
        guard let data = try? JSONEncoder().encode(self) else { return }
        Self.delete()
        var q = Self.query
        q[kSecValueData as String] = data
        // Readable in the background after the first unlock, for reconnecting.
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(q as CFDictionary, nil)
    }

    static func delete() {
        SecItemDelete(query as CFDictionary)
    }
}
