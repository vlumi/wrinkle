import Foundation
import SwiftData

/// A contact the user marked as verified, with the fingerprint their safety number had then.
/// Kept on this device only.
@Model
public final class StoredVerification {
    @Attribute(.unique) public var userID: IDHex
    public var fingerprint: Data
    public var verifiedAt: Date

    public init(userID: IDHex, fingerprint: Data) {
        self.userID = userID
        self.fingerprint = fingerprint
        self.verifiedAt = Date()
    }
}

// MARK: Verification

extension Store {
    public func verification(_ userID: IDHex) -> StoredVerification? {
        var d = FetchDescriptor<StoredVerification>(predicate: #Predicate { $0.userID == userID })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }

    /// Marks the contact verified at the given fingerprint.
    public func verify(_ userID: IDHex, fingerprint: Data) {
        if let v = verification(userID) {
            v.fingerprint = fingerprint
            v.verifiedAt = Date()
        } else {
            context.insert(StoredVerification(userID: userID, fingerprint: fingerprint))
        }
    }

    public func unverify(_ userID: IDHex) {
        if let v = verification(userID) { context.delete(v) }
    }
}

/// What a contact's safety number means to the user.
public enum VerificationState: Equatable, Sendable {
    case unverified
    case verified
    /// Verified earlier, but their devices changed since.
    case changed

    public init(verified: Data?, current: Data?) {
        guard let verified else {
            self = .unverified
            return
        }
        guard let current else {
            self = .verified
            return
        }
        self = verified == current ? .verified : .changed
    }
}
