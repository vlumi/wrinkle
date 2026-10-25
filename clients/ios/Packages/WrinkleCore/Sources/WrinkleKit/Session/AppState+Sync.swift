import Foundation
import WrinkleCore

// MARK: Sync

/// Sync's cursor is older than the oldest event the user's log keeps.
struct CursorExpired: Error {}

extension AppState {
    /// The open connection and the store, or an error when not connected.
    func connected() throws -> (Connection, Store) {
        guard let connection, let store, me != nil else { throw AccountError("Not connected.") }
        return (connection, store)
    }

    /// The log no longer reaches back to the cursor: the next connection starts again as a
    /// device without history, from the head, and lists everything anew.
    func startOver() {
        store?.resetHistory()
        advanceCursor(to: 0)
    }

    func advanceCursor(to next: UInt64) {
        cursor = next
        if let credentials { SavedSession.setCursor(cursor, for: credentials.deviceID) }
    }

    func refreshConversations(_ conn: Connection) async throws {
        let list = try await conn.listConversations()
        guard let store else { return }
        for c in list { _ = store.upsert(c) }
        store.save()
        await openLocked(conn)
    }

    func syncFromCursor(_ conn: Connection) async throws {
        guard let store, me != nil else { return }
        while true {
            let resp: Wrinkle_V1_SyncResponse
            do {
                resp = try await conn.sync(cursor: cursor, limit: Self.syncLimit)
            } catch let e as BackendError where e.code == .cursorExpired {
                throw CursorExpired()
            }
            for e in resp.events { apply(e, store) }
            store.save()
            refreshMembers(after: resp.events, store)
            advanceCursor(to: resp.cursor)
            if !resp.hasMore_p { break }
        }
        try await conn.ack(cursor: cursor)
        await openLocked(conn)
    }

    @discardableResult
    func apply(_ event: Wrinkle_V1_Event, _ store: Store) -> StoredConversation? {
        switch event.body {
        case .profile(let p) where p.user.userID.hex == myUserID:
            me = p.user
        case .device(let d):
            followDevice(d)
        case .palette(let p):
            palette = p.stickerIds
        case .contact(let c):
            followContact(c.contact)
        case .messageDeleted(let d):
            onMessageDeleted?(d.conversationID.hex, d.seq)
        // Synced from before the deletion: the message arrives already deleted.
        case .message(let m) where m.message.hasDeletedAt:
            onMessageDeleted?(m.message.conversationID.hex, m.message.seq)
        default:
            break
        }
        return store.apply(event, me: myUserID)
    }

    func onEvent(_ event: Wrinkle_V1_Event, _ conn: Connection) async {
        guard let store, me != nil else { return }
        // Pushes and sync overlap; the cursor tells them apart. A gap means
        // something was missed: sync fills it.
        guard event.cursor > cursor else { return }
        if event.cursor != cursor + 1 {
            do {
                try await syncFromCursor(conn)
            } catch is CursorExpired {
                startOver()
                await conn.close()
            } catch {}
            return
        }
        if apply(event, store) == nil, case .message = event.body {
            // The first message of a conversation the peer opened.
            try? await refreshConversations(conn)
        }
        store.save()
        refreshMembers(after: [event], store)
        advanceCursor(to: event.cursor)
        try? await conn.ack(cursor: cursor)
        if case .message(let m) = event.body, case .sealed = m.message.content {
            await openLocked(conn)
        } else if case .reaction(let r) = event.body, r.hasSealed {
            await openLocked(conn)
        }
    }
}
