import Foundation

/// What the device remembers between launches outside the Keychain: the cursor into the user's
/// log, per device, and the server last used, to fill in the sign-in form.
enum SavedSession {
    private static var defaults: UserDefaults { .standard }
    private static let versionKey = "wrinkle.version"
    private static let urlKey = "wrinkle.url"
    private static let version = 2

    private static func cursorKey(_ deviceID: UInt64) -> String { "wrinkle.cursor.\(deviceID)" }

    /// Drops what earlier versions saved, keyed by user name and with integer ids.
    static func migrate() {
        guard defaults.integer(forKey: versionKey) != version else { return }
        for key in defaults.dictionaryRepresentation().keys where key.hasPrefix("wrinkle.") {
            defaults.removeObject(forKey: key)
        }
        defaults.set(version, forKey: versionKey)
    }

    static func cursor(for deviceID: UInt64) -> UInt64 {
        UInt64(defaults.string(forKey: cursorKey(deviceID)) ?? "0") ?? 0
    }

    static func setCursor(_ cursor: UInt64, for deviceID: UInt64) {
        defaults.set(String(cursor), forKey: cursorKey(deviceID))
    }

    static func forgetCursor(for deviceID: UInt64) {
        defaults.removeObject(forKey: cursorKey(deviceID))
    }

    static var lastURL: String? { defaults.string(forKey: urlKey) }

    static func setLastURL(_ url: String) { defaults.set(url, forKey: urlKey) }

    private static func decidedKey(_ deviceID: UInt64) -> String { "wrinkle.decided.\(deviceID)" }

    /// The user's other devices whose history sharing this device has settled.
    static func decidedDevices(for deviceID: UInt64) -> Set<UInt64> {
        let ids = defaults.stringArray(forKey: decidedKey(deviceID)) ?? []
        return Set(ids.compactMap { UInt64($0) })
    }

    static func decide(_ device: UInt64, for deviceID: UInt64) {
        let ids = decidedDevices(for: deviceID).union([device])
        defaults.set(ids.map(String.init), forKey: decidedKey(deviceID))
    }

    static func forgetDecidedDevices(for deviceID: UInt64) {
        defaults.removeObject(forKey: decidedKey(deviceID))
    }
}
