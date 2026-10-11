import Foundation
import Security
import os

/// Where the device keeps secrets: named blobs of bytes.
public protocol Vault: Sendable {
    func get(_ account: String) -> Data?
    func set(_ account: String, _ data: Data)
    func delete(_ account: String)
    /// Every stored account name.
    func accounts() -> [String]
}

/// Secrets in the Keychain, readable after the first unlock so the app reconnects and the
/// notification service extension decrypts in the background. With an access group (the app
/// group) the app and the extension share the items; a build without the entitlement, such as
/// an unsigned simulator build, falls back to the app's own Keychain.
public struct KeychainVault: Vault {
    let service: String
    let accessGroup: String?

    public init(service: String, accessGroup: String?) {
        self.service = service
        self.accessGroup = Self.usable(accessGroup, service: service)
    }

    /// The access group if the app may use it.
    private static func usable(_ group: String?, service: String) -> String? {
        guard let group, !group.isEmpty else { return nil }
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccessGroup as String: group,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        q[kSecUseDataProtectionKeychain as String] = true
        let status = SecItemCopyMatching(q as CFDictionary, nil)
        return status == errSecMissingEntitlement ? nil : group
    }

    private func query(_ account: String?) -> [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
        ]
        if let account { q[kSecAttrAccount as String] = account }
        if let accessGroup {
            q[kSecAttrAccessGroup as String] = accessGroup
            q[kSecUseDataProtectionKeychain as String] = true
        }
        return q
    }

    public func get(_ account: String) -> Data? {
        var q = query(account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &item) == errSecSuccess else { return nil }
        return item as? Data
    }

    public func set(_ account: String, _ data: Data) {
        let update = [kSecValueData as String: data]
        if SecItemUpdate(query(account) as CFDictionary, update as CFDictionary) == errSecSuccess {
            return
        }
        var q = query(account)
        q[kSecValueData as String] = data
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(q as CFDictionary, nil)
    }

    public func delete(_ account: String) {
        SecItemDelete(query(account) as CFDictionary)
    }

    public func accounts() -> [String] {
        var q = query(nil)
        q[kSecReturnAttributes as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitAll
        var items: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &items) == errSecSuccess,
            let list = items as? [[String: Any]]
        else { return [] }
        return list.compactMap { $0[kSecAttrAccount as String] as? String }
    }
}

/// Secrets in memory, for tests.
public final class MemoryVault: Vault {
    private let items = OSAllocatedUnfairLock(initialState: [String: Data]())

    public init() {}

    public func get(_ account: String) -> Data? { items.withLock { $0[account] } }
    public func set(_ account: String, _ data: Data) { items.withLock { $0[account] = data } }
    public func delete(_ account: String) { _ = items.withLock { $0.removeValue(forKey: account) } }
    public func accounts() -> [String] { items.withLock { Array($0.keys) } }
}
