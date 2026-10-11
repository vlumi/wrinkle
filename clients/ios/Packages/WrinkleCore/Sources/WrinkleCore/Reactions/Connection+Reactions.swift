import Foundation

// MARK: Reactions

extension Connection {
    /// Sets the user's reactions to a message, already sealed; nil removes them. See
    /// KeyManager.react.
    public func setReaction(conversationID: Data, seq: UInt64, sealed: Wrinkle_V1_Sealed?)
        async throws
    {
        _ = try await call(
            .setReaction(
                .with {
                    $0.conversationID = conversationID
                    $0.seq = seq
                    if let sealed { $0.sealed = sealed }
                }))
    }
}
