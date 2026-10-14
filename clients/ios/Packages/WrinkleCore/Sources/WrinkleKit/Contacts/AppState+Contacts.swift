import Foundation
import WrinkleCore

// MARK: Contacts and blocking

extension AppState {
    /// How another user stands to the user.
    public func contactState(of userID: IDHex) -> Wrinkle_V1_ContactState {
        contacts.state(of: userID)
    }

    /// Asks a user by name to be a contact, or accepts their request.
    public func addContact(name: String) async throws {
        let (connection, _) = try connected()
        followContact(try await connection.addContact(name: name))
    }

    public func addContact(_ userID: IDHex) async throws {
        let (connection, _) = try connected()
        followContact(try await connection.addContact(userID: try wireID(userID)))
    }

    /// Removes a contact, declines their request or withdraws the user's own; a block stays.
    public func removeContact(_ userID: IDHex) async throws {
        let (connection, _) = try connected()
        try await connection.removeContact(userID: try wireID(userID))
        if !contacts.isBlocked(userID) { contacts.set(.unspecified, for: userID) }
    }

    public func blockUser(_ user: Wrinkle_V1_User) async throws {
        let (connection, _) = try connected()
        try await connection.blockUser(userID: user.userID)
        contacts.set(.blocked, for: user.userID.hex, user: user)
        publishNames()
    }

    public func unblockUser(_ userID: IDHex) async throws {
        let (connection, _) = try connected()
        try await connection.unblockUser(userID: try wireID(userID))
        if contacts.isBlocked(userID) { contacts.set(.unspecified, for: userID) }
        publishNames()
    }

    /// Takes a user's state from an event or a response; the event that follows a response
    /// says the same.
    func followContact(_ contact: Wrinkle_V1_Contact) {
        let wasBlocked = contacts.isBlocked(contact.user.userID.hex)
        contacts.apply(contact)
        if wasBlocked != (contact.state == .blocked) { publishNames() }
    }
}

extension AppState {
    /// The direct conversation with a user: the one the device knows, or opened by name.
    public func openConversation(with user: Wrinkle_V1_User) async throws -> IDHex {
        if let c = store?.directConversation(with: user.userID.hex) { return c.id }
        return try await openConversation(peerName: user.name)
    }
}
