import Foundation
import SwiftData

/// The users the user has any contact state with, by user id: from ListContacts after every
/// connect, then kept current by contact events and the user's own requests. A contact whose
/// state is unspecified leaves the book.
public struct ContactBook: Equatable, Sendable {
    public private(set) var entries: [IDHex: Wrinkle_V1_Contact] = [:]

    public init(_ contacts: [Wrinkle_V1_Contact] = []) {
        for c in contacts { apply(c) }
    }

    /// Takes a user's state as the latest: kept, or removed when unspecified.
    public mutating func apply(_ contact: Wrinkle_V1_Contact) {
        let id = contact.user.userID.hex
        if contact.state == .unspecified {
            entries[id] = nil
        } else {
            entries[id] = contact
        }
    }

    /// Sets a user's state, keeping the profile the book has unless one is given.
    public mutating func set(
        _ state: Wrinkle_V1_ContactState, for userID: IDHex, user: Wrinkle_V1_User? = nil
    ) {
        var contact = entries[userID] ?? .with { $0.user.userID = Data(hex: userID) ?? Data() }
        if let user { contact.user = user }
        contact.state = state
        contact.updatedAt = .init(date: Date())
        apply(contact)
    }

    public func state(of userID: IDHex) -> Wrinkle_V1_ContactState {
        entries[userID]?.state ?? .unspecified
    }

    public func isBlocked(_ userID: IDHex) -> Bool { state(of: userID) == .blocked }

    public var blocked: Set<IDHex> { Set(entries.filter { $0.value.state == .blocked }.keys) }

    /// The users in a state, by name.
    public func users(_ state: Wrinkle_V1_ContactState) -> [Wrinkle_V1_User] {
        entries.values.filter { $0.state == state }.map(\.user).sorted {
            ($0.name.lowercased(), $0.userID.hex) < ($1.name.lowercased(), $1.userID.hex)
        }
    }

    /// Requests from others awaiting the user's answer.
    public var incomingCount: Int { entries.values.count { $0.state == .incoming } }

    /// Whether a message's sender is hidden: someone the user blocked, in a group. A direct
    /// conversation with them shows its history; they can no longer write to it.
    public func hides(_ senderID: IDHex, inGroup: Bool) -> Bool {
        inGroup && isBlocked(senderID)
    }

    /// Contacts whose name or the filter matches, ignoring case, leaving out the given users.
    public func pickable(_ filter: String, excluding: Set<IDHex> = []) -> [Wrinkle_V1_User] {
        let f = filter.trimmingCharacters(in: .whitespaces)
        return users(.contact).filter { u in
            !excluding.contains(u.userID.hex) && !u.deleted
                && (f.isEmpty || u.name.localizedCaseInsensitiveContains(f))
        }
    }
}

// MARK: Direct conversations

extension Store {
    /// The direct conversation with a user, if the device knows one.
    public func directConversation(with peerID: IDHex) -> StoredConversation? {
        let direct = Wrinkle_V1_ConversationKind.direct.rawValue
        var d = FetchDescriptor<StoredConversation>(
            predicate: #Predicate { $0.peerID == peerID && $0.kind == direct })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }
}
