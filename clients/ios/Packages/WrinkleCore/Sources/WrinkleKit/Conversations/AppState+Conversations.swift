import Foundation
import WrinkleCore

// MARK: Actions

extension AppState {
    func wireID(_ hex: IDHex) throws -> Data {
        guard let id = Data.id(hex: hex) else { throw AccountError("Unknown conversation.") }
        return id
    }

    public func openConversation(peerName: String) async throws -> IDHex {
        let (connection, store) = try connected()
        let c = try await connection.openConversation(peerName: peerName)
        let stored = store.upsert(c)
        store.save()
        return stored.id
    }

    public func loadHistory(conversationID: IDHex, before: Int64?) async throws {
        let (connection, store) = try connected()
        let page = try await connection.getMessagesPage(
            conversationID: try wireID(conversationID), beforeSeq: UInt64(before ?? 0),
            limit: Self.historyPage)
        for m in page.messages {
            store.insert(m)
            store.replaceReactions(m)
        }
        if !page.hasMore_p, let c = store.conversation(conversationID) {
            c.historyComplete = true
        }
        store.save()
        await openLocked(connection)
    }

    public func send(text: String, in conversationID: IDHex) async throws {
        let (connection, store) = try connected()
        guard let keys, let c = store.conversation(conversationID) else {
            throw AccountError("Not connected.")
        }
        let m = try await keys.send(
            text, to: try wireID(conversationID), members: try members(of: c),
            clientMessageID: UUID().uuidString, connection)
        store.insert(m)
        if let c = store.conversation(conversationID), c.noteLatest(m) {
            c.readSeq = Int64(m.seq)
        }
        store.save()
    }

    /// Who seals for the conversation: the peer of a direct one, a group's members, or the
    /// user alone in their notes.
    func members(of c: StoredConversation) throws -> KeyManager.Members {
        if c.isGroup { return .group }
        if c.isNotes { return .notes }
        return .direct(peer: try wireID(c.peerID))
    }

    /// Deletes one of the user's own messages for everyone; the event that follows changes
    /// nothing more.
    public func deleteMessage(_ seq: Int64, in conversationID: IDHex) async throws {
        let (connection, store) = try connected()
        try await connection.deleteMessage(
            conversationID: try wireID(conversationID), seq: UInt64(seq))
        store.markDeleted(seq, in: conversationID, at: Date())
        store.save()
    }

    /// Marks the conversation read up to its last message, if anything is unread.
    public func markRead(conversationID: IDHex) {
        guard let session = try? connected() else { return }
        let (connection, store) = session
        guard let id = Data.id(hex: conversationID), let c = store.conversation(conversationID),
            c.unread > 0
        else { return }
        let seq = c.lastSeq
        c.readSeq = seq
        store.save()
        Task { try? await connection.markRead(conversationID: id, seq: UInt64(seq)) }
    }
}
