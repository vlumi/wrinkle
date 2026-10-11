import Foundation
import WrinkleCore

// MARK: Reactions

extension AppState {
    /// Toggles one of the user's reactions to a message: the emoji joins their set, or leaves
    /// it if there; the whole set is sealed and replaces the earlier one.
    public func toggleReaction(_ emoji: String, to seq: Int64, in conversationID: IDHex)
        async throws
    {
        let (connection, store) = try connected()
        guard let keys, let c = store.conversation(conversationID) else {
            throw AccountError("Not connected.")
        }
        let own = store.reaction(of: myUserID, to: seq, in: conversationID)?.emoji ?? []
        guard let next = ReactionRules.toggle(emoji, in: own) else {
            throw AccountError(
                ReactionRules.isEmoji(emoji)
                    ? "At most \(ReactionRules.maxPerUser) reactions per message."
                    : "Enter one emoji.")
        }
        try await keys.react(
            next, to: UInt64(seq), in: try wireID(conversationID), members: try members(of: c),
            connection)
        store.setOwnReaction(next, of: myUserID, to: seq, in: conversationID)
        store.save()
    }
}
