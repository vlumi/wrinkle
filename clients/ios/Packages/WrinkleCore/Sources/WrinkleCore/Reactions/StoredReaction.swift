import Foundation
import SwiftData

/// One user's reactions to a message, as the device last saw them.
@Model
public final class StoredReaction {
    #Unique<StoredReaction>([\.conversationID, \.seq, \.userID])
    public var conversationID: IDHex
    public var seq: Int64
    public var userID: IDHex
    /// The opened emoji; empty while sealed.
    public var emoji: [String]
    /// The serialized Sealed while it is not opened.
    public var sealed: Data?
    public var updatedAt: Date

    init(conversationID: IDHex, seq: Int64, userID: IDHex) {
        self.conversationID = conversationID
        self.seq = seq
        self.userID = userID
        self.emoji = []
        self.updatedAt = Date()
    }

    public var isLocked: Bool { sealed != nil }
}

// MARK: Reactions

extension Store {
    public func reactions(to seq: Int64, in conversationID: IDHex) -> [StoredReaction] {
        let d = FetchDescriptor<StoredReaction>(
            predicate: #Predicate { $0.conversationID == conversationID && $0.seq == seq },
            sortBy: [SortDescriptor(\.updatedAt), SortDescriptor(\.userID)])
        return (try? context.fetch(d)) ?? []
    }

    public func reaction(of userID: IDHex, to seq: Int64, in conversationID: IDHex)
        -> StoredReaction?
    {
        var d = FetchDescriptor<StoredReaction>(
            predicate: #Predicate {
                $0.conversationID == conversationID && $0.seq == seq && $0.userID == userID
            })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }

    /// Sets a user's reactions to a message; nil removes them.
    public func setReaction(
        of userID: IDHex, to seq: Int64, in conversationID: IDHex, sealed: Wrinkle_V1_Sealed?,
        at date: Date
    ) {
        let existing = reaction(of: userID, to: seq, in: conversationID)
        guard let sealed else {
            if let existing { context.delete(existing) }
            return
        }
        let row = existing ?? inserted(of: userID, to: seq, in: conversationID)
        row.emoji = []
        row.sealed = try? sealed.serializedData()
        row.updatedAt = date
        open(row)
    }

    /// Sets the user's own reactions as just chosen, opened.
    public func setOwnReaction(
        _ emoji: [String], of userID: IDHex, to seq: Int64, in conversationID: IDHex
    ) {
        let existing = reaction(of: userID, to: seq, in: conversationID)
        guard !emoji.isEmpty else {
            if let existing { context.delete(existing) }
            return
        }
        let row = existing ?? inserted(of: userID, to: seq, in: conversationID)
        row.emoji = emoji
        row.sealed = nil
        row.updatedAt = Date()
    }

    private func inserted(of userID: IDHex, to seq: Int64, in conversationID: IDHex)
        -> StoredReaction
    {
        let r = StoredReaction(conversationID: conversationID, seq: seq, userID: userID)
        context.insert(r)
        return r
    }

    func apply(_ r: Wrinkle_V1_ReactionEvent) -> StoredConversation? {
        let id = r.conversationID.hex
        setReaction(
            of: r.userID.hex, to: Int64(r.seq), in: id, sealed: r.hasSealed ? r.sealed : nil,
            at: r.updatedAt.date)
        return conversation(id)
    }

    /// Replaces a message's reactions with those the server returned with it.
    public func replaceReactions(_ m: Wrinkle_V1_Message) {
        let id = m.conversationID.hex
        let seq = Int64(m.seq)
        let kept = m.hasDeletedAt ? [] : m.reactions
        let users = Set(kept.map(\.userID.hex))
        for r in reactions(to: seq, in: id) where !users.contains(r.userID) {
            context.delete(r)
        }
        for r in kept {
            setReaction(
                of: r.userID.hex, to: seq, in: id, sealed: r.sealed, at: r.updatedAt.date)
        }
    }

    /// Forgets the reactions to a deleted message.
    func dropReactions(to seq: Int64, in conversationID: IDHex) {
        for r in reactions(to: seq, in: conversationID) { context.delete(r) }
    }

    /// Opens sealed reactions if a held key does; ones that never will stay hidden.
    public func open(_ r: StoredReaction) {
        guard let keys, let data = r.sealed,
            let sealed = try? Wrinkle_V1_Sealed(serializedBytes: data),
            let conversationID = Data.id(hex: r.conversationID), let userID = Data(hex: r.userID),
            let reactions = keys.open(
                sealed, reactionsOf: userID, to: UInt64(r.seq), in: conversationID)
        else { return }
        r.emoji = ReactionRules.valid(reactions.emoji)
        r.sealed = nil
    }

    /// The epochs that locked reactions need, by conversation.
    func lockedReactions() -> [IDHex: Set<UInt32>] {
        let d = FetchDescriptor<StoredReaction>(predicate: #Predicate { $0.sealed != nil })
        var need: [IDHex: Set<UInt32>] = [:]
        for r in (try? context.fetch(d)) ?? [] {
            guard let data = r.sealed, let s = try? Wrinkle_V1_Sealed(serializedBytes: data)
            else { continue }
            need[r.conversationID, default: []].insert(s.epoch)
        }
        return need
    }

    /// Tries the held keys on the conversation's locked reactions.
    func reopenReactions(_ conversationID: IDHex) {
        let d = FetchDescriptor<StoredReaction>(
            predicate: #Predicate { $0.conversationID == conversationID && $0.sealed != nil })
        for r in (try? context.fetch(d)) ?? [] { open(r) }
    }
}
