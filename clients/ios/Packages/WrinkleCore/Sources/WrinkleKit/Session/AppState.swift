import CoreGraphics
import Foundation
import SwiftData
import SwiftUI
import WrinkleCore

/// Signing in, or creating an account.
public enum AccountMode: Hashable, Sendable {
    case signIn, register
}

/// The device's session: its account, the cursor into the user's log, and the flow of events
/// into the store. Reconnects with backoff using the device token kept in the Keychain; after a
/// reconnect it syncs from the stored cursor, so the device fetches only what it missed.
@MainActor
@Observable
public final class AppState {
    public init() {}

    /// The user as the backend last described them; nil until the first Connect.
    public internal(set) var me: Wrinkle_V1_User?
    public private(set) var state: ConnectionState = .offline
    /// The last connection failure, for diagnostics.
    public private(set) var lastError: String?
    /// Why the device was returned to the sign-in screen, if not by its own choice.
    public var notice: String?
    /// A conversation to show, set when a notification is tapped.
    public var pendingConversation: IDHex?
    /// Told of each message deleted, by conversation id and seq, so the app can withdraw a
    /// notification already shown for it.
    public var onMessageDeleted: (@MainActor (IDHex, UInt64) -> Void)?
    /// The account's signed-in devices, once listed; kept current by device events.
    public internal(set) var devices: [Wrinkle_V1_Device] = []
    /// Avatars decoded so far, by blob id as hex.
    var avatarImages: [String: CGImage] = [:]
    /// Another device of the user that set its key and awaits the user's decision on sharing
    /// the message history with it.
    public internal(set) var newDevice: Wrinkle_V1_Device?
    /// Notices about the account's security, such as a device signing in, until dismissed.
    public internal(set) var securityNotices: [String] = []
    /// Users' fingerprints as last known, by user id, for the safety number warnings.
    public internal(set) var fingerprints: [IDHex: Data] = [:]
    /// The device's encryption state, once connected.
    var keys: KeyManager?
    /// Epochs whose keys were asked for this connection without success, by conversation.
    var triedEpochs: [IDHex: Set<UInt32>] = [:]
    let deviceVault = Vaults.device()
    let sharedVault = Vaults.shared()

    private var credentials: Credentials?
    private var connection: Connection?
    private(set) var store: Store?
    private var cursor: UInt64 = 0
    private var sessionTask: Task<Void, Never>?
    private var pushToken: (service: Wrinkle_V1_PushService, token: String)?

    /// The simulator reaches the backend on the Mac directly.
    public static let defaultURL = "ws://localhost:8080/v1/ws"
    /// A device on the network goes through the TLS edge on the Mac.
    public static let networkURLHint = "wss://<mac>.local:8443/v1/ws"
    private static let syncLimit: UInt32 = 200
    static let historyPage: UInt32 = 50
    private static let signOutWait: Duration = .seconds(3)
    private static let rejectedNotice =
        "This device was signed out, perhaps by a password change elsewhere. Sign in again."
    private static let signedOutNotice = "Signed out."
    private static let keyLostNotice =
        "This device lost its encryption key and was signed out. Sign in again."
    static let deletedNotice = "Account deleted."

    /// The device's name, without UIKit so the package compiles for the Mac too.
    static var deviceName: String {
        #if canImport(UIKit)
        AccountRules.deviceName(ProcessInfo.processInfo.hostName)
        #else
        AccountRules.deviceName(Host.current().localizedName ?? "mac")
        #endif
    }

    public var isSignedIn: Bool { credentials != nil }

    /// The signed-in user's id, or empty when signed out.
    var myUserID: IDHex { credentials?.userID.hex ?? "" }

    /// This device's id, or nil when signed out.
    var myDeviceID: UInt64? { credentials?.deviceID }

    /// Where the server's blobs are read and uploaded.
    var blobBase: URL? {
        credentials.flatMap { URL(string: $0.serverURL) }.flatMap { BlobAddress.base(gateway: $0) }
    }

    /// The server last used, to fill in the sign-in form.
    public var lastURL: String { credentials?.serverURL ?? SavedSession.lastURL ?? Self.defaultURL }

    /// Resumes the saved session, if there is one.
    public func resume(context: ModelContext) {
        if store == nil { store = Store(context: context, keys: EpochKeys(vault: sharedVault)) }
        SavedSession.migrate()
        guard credentials == nil, let saved = Credentials.load() else { return }
        credentials = saved
        cursor = SavedSession.cursor(for: saved.deviceID)
        startSession(first: nil)
    }

    /// Registers or signs in on a new connection, then keeps it as the session's first.
    public func authenticate(_ mode: AccountMode, name: String, password: String, url: String)
        async throws
    {
        guard let wsURL = URL(string: url), ["ws", "wss"].contains(wsURL.scheme?.lowercased())
        else { throw AccountError("Enter a ws:// or wss:// server URL.") }
        stopSession()
        let conn = Connection(url: wsURL)
        do {
            try await conn.open()
        } catch {
            await conn.close()
            throw AccountError("Cannot reach the server at \(url).")
        }
        let creds: Wrinkle_V1_DeviceCredentials
        do {
            switch mode {
            case .register:
                creds = try await conn.register(
                    name: name, password: password, deviceName: Self.deviceName)
            case .signIn:
                creds = try await conn.signIn(
                    name: name, password: password, deviceName: Self.deviceName)
            }
        } catch {
            await conn.close()
            throw error
        }
        // A new device: whatever an earlier account left here goes.
        store?.reset()
        forgetEncryption(deviceID: credentials?.deviceID)
        let saved = Credentials(
            userID: creds.userID, deviceID: creds.deviceID, deviceToken: creds.deviceToken,
            serverURL: url)
        saved.save()
        SavedSession.setLastURL(url)
        credentials = saved
        cursor = 0
        notice = nil
        lastError = nil
        startSession(first: conn)
    }

    /// Signs this device out on the backend, if it can within a few seconds, and forgets the
    /// account and everything stored for it.
    public func signOut() async {
        if let connection, me != nil {
            let request = Task { try? await connection.signOut() }
            let deadline = Task {
                try? await Task.sleep(for: Self.signOutWait)
                await connection.close()
            }
            await request.value
            deadline.cancel()
        }
        endSession(notice: nil)
    }

    private func startSession(first: Connection?) {
        sessionTask = Task { await self.runSession(first: first) }
    }

    private func stopSession() {
        sessionTask?.cancel()
        sessionTask = nil
        if let c = connection { Task { await c.close() } }
        connection = nil
        state = .offline
    }

    /// Back to the sign-in screen: the Keychain entry, the cursor and the local store go.
    func endSession(notice: String?) {
        stopSession()
        if let credentials { SavedSession.forgetCursor(for: credentials.deviceID) }
        forgetEncryption(deviceID: credentials?.deviceID)
        Credentials.delete()
        credentials = nil
        me = nil
        cursor = 0
        pendingConversation = nil
        devices = []
        store?.reset()
        self.notice = notice
    }

    /// Runs connections until cancelled. `first` is already open, from signing in.
    private func runSession(first: Connection?) async {
        var opened = first
        var backoff: Duration = .milliseconds(500)
        while !Task.isCancelled, let creds = credentials, let url = URL(string: creds.serverURL) {
            let conn = opened ?? Connection(url: url)
            let isOpen = opened != nil
            opened = nil
            connection = conn
            let stateTask = Task { for await s in conn.states { self.state = s } }
            do {
                if !isOpen { try await conn.open() }
                backoff = .milliseconds(500)
                try await onOpen(conn, creds)
                for await event in conn.events {
                    if Task.isCancelled { break }
                    await onEvent(event, conn)
                }
            } catch is TokenRejected {
                stateTask.cancel()
                await conn.close()
                endSession(notice: Self.rejectedNotice)
                return
            } catch is KeyLost {
                stateTask.cancel()
                try? await conn.signOut()
                await conn.close()
                endSession(notice: Self.keyLostNotice)
                return
            } catch {
                lastError = "\(error)"
            }
            stateTask.cancel()
            // Signed out elsewhere, or the account deleted: no reconnecting.
            if !Task.isCancelled, await conn.closeCode == CloseCode.signedOut {
                await conn.close()
                endSession(notice: Self.signedOutNotice)
                return
            }
            await conn.close()
            state = .offline
            if Task.isCancelled { break }
            try? await Task.sleep(for: backoff)
            backoff = min(backoff * 2, .seconds(10))
        }
    }

    private struct TokenRejected: Error {}

    private func onOpen(_ conn: Connection, _ creds: Credentials) async throws {
        let resp: Wrinkle_V1_ConnectResponse
        do {
            resp = try await conn.connect(deviceToken: creds.deviceToken)
        } catch let e as BackendError where e.code == .unauthenticated {
            throw TokenRejected()
        }
        me = resp.user
        try await startEncryption(conn, creds)
        if cursor == 0 {
            // A device without history starts at the head; the past comes from the
            // conversation list and history requests.
            advanceCursor(to: resp.logHead)
        }
        if let pushToken {
            try await conn.registerPushToken(service: pushToken.service, token: pushToken.token)
        }
        try await refreshConversations(conn)
        if !devices.isEmpty { devices = try await conn.listDevices() }
        try await syncFromCursor(conn)
        publishNames()
    }
}

// MARK: Lifecycle

extension AppState {
    /// The app went to the background: close the connection, so the backend
    /// sees the device as gone and pushes to it instead. iOS would suspend the
    /// socket within seconds anyway, leaving it half-open.
    public func enterBackground() {
        guard sessionTask != nil else { return }
        stopSession()
    }

    /// The app is active again: reconnect and sync what was missed.
    public func enterForeground() {
        guard credentials != nil, sessionTask == nil else { return }
        startSession(first: nil)
    }

    /// The token APNs issued this device; registered now if connected, and
    /// after every connect.
    public func setPushToken(_ token: String, sandbox: Bool) {
        let service: Wrinkle_V1_PushService = sandbox ? .apnsSandbox : .apns
        pushToken = (service, token)
        if let connection, me != nil {
            Task { try? await connection.registerPushToken(service: service, token: token) }
        }
    }
}

// MARK: Sync

extension AppState {
    /// The open connection and the store, or an error when not connected.
    func connected() throws -> (Connection, Store) {
        guard let connection, let store, me != nil else { throw AccountError("Not connected.") }
        return (connection, store)
    }

    private func advanceCursor(to next: UInt64) {
        cursor = next
        if let credentials { SavedSession.setCursor(cursor, for: credentials.deviceID) }
    }

    private func refreshConversations(_ conn: Connection) async throws {
        let list = try await conn.listConversations()
        guard let store else { return }
        for c in list { _ = store.upsert(c) }
        store.save()
        await openLocked(conn)
    }

    private func syncFromCursor(_ conn: Connection) async throws {
        guard let store, me != nil else { return }
        while true {
            let resp = try await conn.sync(cursor: cursor, limit: Self.syncLimit)
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
    private func apply(_ event: Wrinkle_V1_Event, _ store: Store) -> StoredConversation? {
        switch event.body {
        case .profile(let p) where p.user.userID.hex == myUserID:
            me = p.user
        case .device(let d):
            followDevice(d)
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

    private func onEvent(_ event: Wrinkle_V1_Event, _ conn: Connection) async {
        guard let store, me != nil else { return }
        // Pushes and sync overlap; the cursor tells them apart. A gap means
        // something was missed: sync fills it.
        guard event.cursor > cursor else { return }
        if event.cursor != cursor + 1 {
            try? await syncFromCursor(conn)
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

/// A failure the client itself detected, worded for the user.
public struct AccountError: Error, CustomStringConvertible {
    public let description: String
    init(_ description: String) { self.description = description }
}
