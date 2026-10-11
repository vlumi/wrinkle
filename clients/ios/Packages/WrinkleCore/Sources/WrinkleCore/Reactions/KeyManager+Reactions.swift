import Foundation

// MARK: Reactions

extension KeyManager {
    /// Seals the user's reactions to a message and sets them, replacing earlier ones; none
    /// removes them. A set refused for a stale epoch refreshes and is retried once.
    public func react(
        _ emoji: [String], to seq: UInt64, in conversationID: Data, members: Members,
        _ connection: Connection
    ) async throws {
        guard !emoji.isEmpty else {
            try await connection.setReaction(conversationID: conversationID, seq: seq, sealed: nil)
            return
        }
        let reactions = Wrinkle_V1_Reactions.with { $0.emoji = emoji }
        var (epoch, key) = try await currentEpoch(conversationID, members, connection)
        var retried = false
        while true {
            let sealed = try E2EE.seal(
                reactions, key: key,
                context: E2EE.ReactionContext(
                    conversationID: conversationID, epoch: epoch, userID: userID, seq: seq),
                nonce: E2EE.randomNonce())
            do {
                try await connection.setReaction(
                    conversationID: conversationID, seq: seq, sealed: sealed)
                return
            } catch let e as BackendError where e.code == .failedPrecondition && !retried {
                retried = true
                invalidate(conversationID)
                (epoch, key) = try await currentEpoch(
                    conversationID, members, connection, refresh: true)
            }
        }
    }

    /// Opens a user's sealed reactions to a message with a held key.
    public nonisolated func open(
        _ sealed: Wrinkle_V1_Sealed, reactionsOf userID: Data, to seq: UInt64,
        in conversationID: Data
    ) -> [String]? {
        epochs.open(sealed, reactionsOf: userID, to: seq, in: conversationID)
            .map { ReactionRules.valid($0.emoji) }
    }
}
