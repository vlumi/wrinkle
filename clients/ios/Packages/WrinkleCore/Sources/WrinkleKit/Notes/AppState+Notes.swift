import Foundation
import WrinkleCore

// MARK: Notes

extension AppState {
    /// The user's notes: the ones the device knows, or opened, created on first use.
    public func openNotes() async throws -> IDHex {
        let (connection, store) = try connected()
        if let c = store.notesConversation() { return c.id }
        let stored = store.upsert(try await connection.openNotes())
        store.save()
        return stored.id
    }
}
