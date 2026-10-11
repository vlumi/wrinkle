import Foundation

// MARK: Deleted messages

extension Store {
    /// Applies a deletion to the message, its reactions and, when it is the latest, the list.
    @discardableResult
    public func markDeleted(_ seq: Int64, in conversationID: IDHex, at date: Date)
        -> StoredConversation?
    {
        message(seq, in: conversationID)?.delete(at: date)
        dropReactions(to: seq, in: conversationID)
        guard let c = conversation(conversationID) else { return nil }
        if c.lastSeq == seq { c.lastWasDeleted() }
        return c
    }
}
