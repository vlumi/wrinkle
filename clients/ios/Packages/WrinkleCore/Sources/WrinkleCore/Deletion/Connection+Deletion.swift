import Foundation

// MARK: Deleting messages

extension Connection {
    /// Deletes one of the user's own messages for everyone in the conversation.
    public func deleteMessage(conversationID: Data, seq: UInt64) async throws {
        _ = try await call(
            .deleteMessage(
                .with {
                    $0.conversationID = conversationID
                    $0.seq = seq
                }))
    }
}
