import Foundation

// MARK: Contact requests

extension Connection {
    public func listContacts() async throws -> [Wrinkle_V1_Contact] {
        try await call(.listContacts(Wrinkle_V1_ListContactsRequest())).listContacts.contacts
    }

    /// Asks a user to be a contact, or accepts their request; returns how they now stand.
    public func addContact(name: String) async throws -> Wrinkle_V1_Contact {
        try await call(.addContact(.with { $0.name = name })).addContact.contact
    }

    public func addContact(userID: Data) async throws -> Wrinkle_V1_Contact {
        try await call(.addContact(.with { $0.userID = userID })).addContact.contact
    }

    /// Removes a contact, declines a request or withdraws one's own.
    public func removeContact(userID: Data) async throws {
        _ = try await call(.removeContact(.with { $0.userID = userID }))
    }

    public func blockUser(userID: Data) async throws {
        _ = try await call(.blockUser(.with { $0.userID = userID }))
    }

    public func unblockUser(userID: Data) async throws {
        _ = try await call(.unblockUser(.with { $0.userID = userID }))
    }
}
