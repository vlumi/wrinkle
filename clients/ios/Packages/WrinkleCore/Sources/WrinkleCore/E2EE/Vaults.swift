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

    public static func device() -> Vault {
        KeychainVault(service: "fi.misaki.wrinkle.device", accessGroup: nil)
    }

    public static func shared() -> Vault {
        KeychainVault(service: "fi.misaki.wrinkle.shared", accessGroup: appGroup)
    }
}
