import Foundation

// MARK: Notes

extension Connection {
    /// The user's notes, a conversation with only themselves, created on first use.
    public func openNotes() async throws -> Wrinkle_V1_Conversation {
        try await call(.openNotes(Wrinkle_V1_OpenNotesRequest())).openNotes.conversation
    }
}
