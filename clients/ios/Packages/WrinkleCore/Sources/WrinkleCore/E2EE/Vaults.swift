import Foundation

/// The app's two vaults. The device's private key stays in the app's own Keychain; the epoch
/// keys and the names the notification service extension words pushes with go to the app
/// group's, which the extension shares.
public enum Vaults {
    /// The app group, from the WrinkleAppGroup key that project.yml puts in both targets'
    /// Info.plist.
    public static var appGroup: String? {
        Bundle.main.object(forInfoDictionaryKey: "WrinkleAppGroup") as? String
    }

    /// The stem of the Keychain services, the same in the app and the extension: the app
    /// group without its "group." prefix, or the bundle id where there is no group.
    public static var identity: String {
        if let appGroup, appGroup.hasPrefix("group.") {
            return String(appGroup.dropFirst("group.".count))
        }
        return Bundle.main.bundleIdentifier ?? "wrinkle"
    }

    public static func device() -> Vault {
        KeychainVault(service: identity + ".device", accessGroup: nil)
    }

    public static func shared() -> Vault {
        KeychainVault(service: identity + ".shared", accessGroup: appGroup)
    }
}
