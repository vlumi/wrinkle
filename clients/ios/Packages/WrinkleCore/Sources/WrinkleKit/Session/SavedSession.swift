import Foundation

/// What the device remembers between launches: per user, the device id the backend assigned and
/// the cursor into the user's log; and the last session, resumed on launch until signed out.
enum SavedSession {
    private static var defaults: UserDefaults { .standard }
    private static let lastName = "wrinkle.last.name"
    private static let lastURL = "wrinkle.last.url"

    private static func key(_ user: String, _ what: String) -> String { "wrinkle.\(user).\(what)" }

    private static func number(_ key: String) -> UInt64 {
        UInt64(defaults.string(forKey: key) ?? "0") ?? 0
    }

    static func deviceID(for user: String) -> UInt64 { number(key(user, "device")) }

    static func setDeviceID(_ id: UInt64, for user: String) {
        defaults.set(String(id), forKey: key(user, "device"))
    }

    static func cursor(for user: String) -> UInt64 { number(key(user, "cursor")) }

    static func setCursor(_ cursor: UInt64, for user: String) {
        defaults.set(String(cursor), forKey: key(user, "cursor"))
    }

    static var last: (name: String, url: String)? {
        guard let name = defaults.string(forKey: lastName),
            let url = defaults.string(forKey: lastURL)
        else { return nil }
        return (name, url)
    }

    static func setLast(name: String, url: String) {
        defaults.set(name, forKey: lastName)
        defaults.set(url, forKey: lastURL)
    }

    /// Keeps the URL, which only resumes together with a name.
    static func forgetLast() {
        defaults.removeObject(forKey: lastName)
    }
}
