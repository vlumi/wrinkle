import Foundation
import SwiftData
import SwiftUI
import WrinkleCore

/// The device's session: sign-in, the cursor into the user's log, and the flow of events into
/// the store. Reconnects with backoff; after a reconnect it syncs from the stored cursor, so the
/// device fetches only what it missed.
@MainActor
@Observable
public final class AppState {
    public init() {}

    public private(set) var userName: String?
    public private(set) var me: Wrinkle_V1_User?
    public private(set) var state: ConnectionState = .offline
    public var lastError: String?

    private var connection: Connection?
    private var store: Store?
    private var cursor: UInt64 = 0
    private var deviceID: UInt64 = 0
    private var sessionTask: Task<Void, Never>?

    public static let defaultURL = "ws://localhost:8080/v1/ws"
    private static let syncLimit: UInt32 = 200
    static let historyPage: UInt32 = 50

    /// The device's name, without UIKit so the package compiles for the Mac too.
    static var deviceName: String {
        #if canImport(UIKit)
        ProcessInfo.processInfo.hostName
        #else
        Host.current().localizedName ?? "mac"
        #endif
    }

    public var isSignedIn: Bool { userName != nil }

    private func key(_ name: String, _ what: String) -> String { "wrinkle.\(name).\(what)" }

    public func signIn(name: String, url: String, context: ModelContext) {
        signOut()
        guard let wsURL = URL(string: url) else {
            lastError = "bad gateway URL"
            return
        }
        userName = name
        store = Store(context: context)
        let defaults = UserDefaults.standard
        deviceID = UInt64(defaults.string(forKey: key(name, "device")) ?? "0") ?? 0
        cursor = UInt64(defaults.string(forKey: key(name, "cursor")) ?? "0") ?? 0
        defaults.set(name, forKey: "wrinkle.last.name")
        defaults.set(url, forKey: "wrinkle.last.url")
        sessionTask = Task { await self.runSession(name: name, url: wsURL) }
    }

    public func signOut() {
        sessionTask?.cancel()
        sessionTask = nil
        if let c = connection { Task { await c.close() } }
        connection = nil
        userName = nil
        me = nil
        state = .offline
        UserDefaults.standard.removeObject(forKey: "wrinkle.last.name")
    }

    /// Resumes the last session, if there was one.
    public func resume(context: ModelContext) {
        let defaults = UserDefaults.standard
        if let name = defaults.string(forKey: "wrinkle.last.name"),
            let url = defaults.string(forKey: "wrinkle.last.url")
        {
            signIn(name: name, url: url, context: context)
        }
    }

    private func runSession(name: String, url: URL) async {
        var backoff: Duration = .milliseconds(500)
        while !Task.isCancelled {
            let conn = Connection(url: url)
            connection = conn
            let stateTask = Task { for await s in conn.states { self.state = s } }
            do {
                try await conn.open()
                backoff = .milliseconds(500)
                try await onOpen(conn, name: name)
                for await event in conn.events {
                    if Task.isCancelled { break }
                    await onEvent(event, conn)
                }
            } catch {
                lastError = "\(error)"
            }
            stateTask.cancel()
            await conn.close()
            state = .offline
            if Task.isCancelled { break }
            try? await Task.sleep(for: backoff)
            backoff = min(backoff * 2, .seconds(10))
        }
    }

    private func onOpen(_ conn: Connection, name: String) async throws {
        let resp = try await conn.connect(
            userName: name, deviceID: deviceID, deviceName: Self.deviceName)
        me = resp.user
        deviceID = resp.deviceID
        UserDefaults.standard.set(String(deviceID), forKey: key(name, "device"))
        if cursor == 0 {
            // A device without history starts at the head; the past comes from the
            // conversation list and history requests.
            cursor = resp.logHead
            persistCursor()
        }
        try await refreshConversations(conn)
        try await syncFromCursor(conn)
    }

    private func persistCursor() {
        if let userName {
            UserDefaults.standard.set(String(cursor), forKey: key(userName, "cursor"))
        }
    }

    private func refreshConversations(_ conn: Connection) async throws {
        let list = try await conn.listConversations()
        guard let store else { return }
        for c in list { _ = store.upsert(c) }
        store.save()
    }

    private func syncFromCursor(_ conn: Connection) async throws {
        guard let store, let me else { return }
        while true {
            let resp = try await conn.sync(cursor: cursor, limit: Self.syncLimit)
            for e in resp.events { _ = store.apply(e, me: Int64(me.userID)) }
            store.save()
            cursor = resp.cursor
            persistCursor()
            if !resp.hasMore_p { break }
        }
        try await conn.ack(cursor: cursor)
    }

    private func onEvent(_ event: Wrinkle_V1_Event, _ conn: Connection) async {
        guard let store, let me else { return }
        // Pushes and sync overlap; the cursor tells them apart. A gap means
        // something was missed: sync fills it.
        guard event.cursor > cursor else { return }
        if event.cursor != cursor + 1 {
            try? await syncFromCursor(conn)
            return
        }
        if store.apply(event, me: Int64(me.userID)) == nil, case .message = event.body {
            // The first message of a conversation the peer opened.
            try? await refreshConversations(conn)
        }
        store.save()
        cursor = event.cursor
        persistCursor()
        try? await conn.ack(cursor: cursor)
    }

    // MARK: Actions

    public func openConversation(peerName: String) async throws -> Int64 {
        guard let connection, let store else { throw URLError(.notConnectedToInternet) }
        let c = try await connection.openConversation(peerUserName: peerName)
        let stored = store.upsert(c)
        store.save()
        return stored.id
    }

    public func loadHistory(conversationID: Int64, before: Int64?) async throws {
        guard let connection, let store else { return }
        let messages = try await connection.getMessages(
            conversationID: UInt64(conversationID), beforeSeq: UInt64(before ?? 0),
            limit: Self.historyPage)
        for m in messages { store.insert(m) }
        store.save()
    }

    public func send(text: String, in conversationID: Int64) async throws {
        guard let connection, let store else { throw URLError(.notConnectedToInternet) }
        let m = try await connection.sendMessage(
            conversationID: UInt64(conversationID), clientMessageID: UUID().uuidString, text: text)
        store.insert(m)
        if let c = store.conversation(conversationID), Int64(m.seq) > c.lastSeq {
            c.lastSeq = Int64(m.seq)
            c.lastSenderID = Int64(m.senderUserID)
            c.lastText = m.text
            c.lastSentAt = m.sentAt.date
            c.updatedAt = m.sentAt.date
            c.readSeq = Int64(m.seq)
        }
        store.save()
    }

    /// Marks the conversation read up to its last message, if anything is unread.
    public func markRead(conversationID: Int64) {
        guard let connection, let store, let c = store.conversation(conversationID), c.unread > 0
        else { return }
        let seq = c.lastSeq
        c.readSeq = seq
        store.save()
        Task {
            try? await connection.markRead(conversationID: UInt64(conversationID), seq: UInt64(seq))
        }
    }
}
