import Foundation

/// The contract's rules for names and passwords, checked before asking the backend so the form
/// can say what is wrong. The backend checks them again.
public enum AccountRules {
    public static let maxNameBytes = 64
    public static let minPasswordBytes = 8
    public static let maxPasswordBytes = 256

    /// What is wrong with a name, or nil if it is acceptable.
    public static func nameProblem(_ name: String) -> String? {
        let bytes = name.utf8.count
        if bytes == 0 { return "Enter a name." }
        if bytes > maxNameBytes { return "A name is at most \(maxNameBytes) bytes." }
        let scalars = name.unicodeScalars
        if let first = scalars.first, let last = scalars.last,
            first.properties.isWhitespace || last.properties.isWhitespace
        {
            return "A name cannot start or end with a space."
        }
        if scalars.contains(where: { $0.properties.generalCategory == .control }) {
            return "A name cannot contain control characters."
        }
        return nil
    }

    /// What is wrong with a new password, or nil if it is acceptable.
    public static func passwordProblem(_ password: String) -> String? {
        let bytes = password.utf8.count
        if bytes < minPasswordBytes { return "A password is at least \(minPasswordBytes) bytes." }
        if bytes > maxPasswordBytes { return "A password is at most \(maxPasswordBytes) bytes." }
        return nil
    }

    /// The name a device registers under: the host name, cut to the contract's 64 bytes
    /// without splitting a character.
    public static func deviceName(_ name: String) -> String {
        var out = ""
        for ch in name {
            if out.utf8.count + String(ch).utf8.count > maxNameBytes { break }
            out.append(ch)
        }
        return out
    }
}
