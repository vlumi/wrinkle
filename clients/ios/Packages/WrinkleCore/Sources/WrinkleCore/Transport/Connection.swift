import Foundation
import SwiftProtobuf

/// An error response from the backend.
public struct BackendError: Error, Equatable, Sendable {
    public let code: Wrinkle_V1_ErrorCode
    public let message: String
    /// Which precondition failed, or which limit was reached; unspecified otherwise.
    public let reason: Wrinkle_V1_ErrorReason

    public init(
        code: Wrinkle_V1_ErrorCode, message: String, reason: Wrinkle_V1_ErrorReason = .unspecified
    ) {
        self.code = code
        self.message = message
        self.reason = reason
    }

    /// The conversation's keys are to be fetched again: the epoch moved on, or must.
    public var staleKeys: Bool { reason == .staleKeyEpoch || reason == .keysMustRotate }
}

public enum ConnectionState: Sendable {
    case offline, connecting, connected
}

/// WebSocket close codes the client acts on (see frame.proto).
public enum CloseCode {
    /// The device was signed out, or the account deleted: forget the credentials and do not
    /// reconnect.
    public static let signedOut = 4001
}

/// One device's WebSocket connection: binary Frames in and out, requests matched to
/// responses by id, events delivered as a stream.
public actor Connection {
    /// The largest frame accepted from the server, above the contract's 1 MiB.
    static let maxFrame = 2 << 20

    private let url: URL
    private let session: URLSession
    private var task: URLSessionWebSocketTask?
    private var nextID: UInt64 = 0
    private var pending: [UInt64: CheckedContinuation<Wrinkle_V1_Response, Error>] = [:]
    /// The close code the server sent, once the socket has closed; nil before or when none.
    public private(set) var closeCode: Int?
    private let eventsContinuation: AsyncStream<Wrinkle_V1_Event>.Continuation
    private let stateContinuation: AsyncStream<ConnectionState>.Continuation

    /// Pushed events, in order. Ends when the connection closes.
    public nonisolated let events: AsyncStream<Wrinkle_V1_Event>
    /// Connection state changes.
    public nonisolated let states: AsyncStream<ConnectionState>

    public init(url: URL) {
        self.url = url
        self.session = URLSession(configuration: .ephemeral)
        (events, eventsContinuation) = AsyncStream.makeStream()
        (states, stateContinuation) = AsyncStream.makeStream()
    }

    /// Opens the socket and starts reading. Returns once the socket is open.
    public func open() async throws {
        stateContinuation.yield(.connecting)
        let task = session.webSocketTask(with: url)
        task.maximumMessageSize = Self.maxFrame
        self.task = task
        task.resume()
        // URLSession reports a failure to open on the first receive, so probe with a
        // ping before considering the connection up.
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            task.sendPing { error in
                if let error { c.resume(throwing: error) } else { c.resume() }
            }
        }
        stateContinuation.yield(.connected)
        Task { await self.readLoop(task) }
    }

    public func close() {
        task?.cancel(with: .normalClosure, reason: nil)
        task = nil
        failPending(URLError(.cancelled))
        stateContinuation.yield(.offline)
        eventsContinuation.finish()
    }

    private func readLoop(_ task: URLSessionWebSocketTask) async {
        while true {
            do {
                let message = try await task.receive()
                guard case .data(let data) = message else { continue }
                let frame = try Wrinkle_V1_Frame(serializedBytes: data)
                dispatch(frame)
            } catch {
                if self.task === task {
                    self.task = nil
                    let code = task.closeCode.rawValue
                    if code != URLSessionWebSocketTask.CloseCode.invalid.rawValue {
                        closeCode = code
                    }
                    failPending(error)
                    stateContinuation.yield(.offline)
                    eventsContinuation.finish()
                }
                return
            }
        }
    }

    private func dispatch(_ frame: Wrinkle_V1_Frame) {
        switch frame.kind {
        case .response(let response):
            if let c = pending.removeValue(forKey: response.requestID) {
                if case .error(let e) = response.body {
                    c.resume(
                        throwing: BackendError(code: e.code, message: e.message, reason: e.reason))
                } else {
                    c.resume(returning: response)
                }
            }
        case .event(let event):
            eventsContinuation.yield(event)
        case .ping:
            var pong = Wrinkle_V1_Frame()
            pong.pong = Wrinkle_V1_Pong()
            Task { try? await self.send(pong) }
        default:
            break
        }
    }

    private func failPending(_ error: Error) {
        for (_, c) in pending { c.resume(throwing: error) }
        pending.removeAll()
    }

    private func failRequest(_ id: UInt64, _ error: Error) {
        pending.removeValue(forKey: id)?.resume(throwing: error)
    }

    public func send(_ frame: Wrinkle_V1_Frame) async throws {
        guard let task else { throw URLError(.notConnectedToInternet) }
        try await task.send(.data(try frame.serializedBytes()))
    }

    /// Sends a request and waits for its response.
    public func call(_ body: Wrinkle_V1_Request.OneOf_Body) async throws -> Wrinkle_V1_Response {
        nextID += 1
        let id = nextID
        var frame = Wrinkle_V1_Frame()
        frame.request = Wrinkle_V1_Request.with {
            $0.id = id
            $0.body = body
        }
        return try await withCheckedThrowingContinuation { c in
            pending[id] = c
            Task {
                do {
                    try await self.send(frame)
                } catch {
                    self.failRequest(id, error)
                }
            }
        }
    }

    public func ack(cursor: UInt64) async throws {
        var frame = Wrinkle_V1_Frame()
        frame.ack = Wrinkle_V1_Ack.with { $0.cursor = cursor }
        try await send(frame)
    }

    // MARK: Requests

    /// Creates an account and signs this device in; Connect with the token follows.
    public func register(name: String, password: String, deviceName: String) async throws
        -> Wrinkle_V1_DeviceCredentials
    {
        try await call(
            .register(
                .with {
                    $0.name = name
                    $0.password = password
                    $0.deviceName = deviceName
                })
        ).register.credentials
    }

    /// Signs this device in to an existing account; Connect with the token follows.
    public func signIn(name: String, password: String, deviceName: String) async throws
        -> Wrinkle_V1_DeviceCredentials
    {
        try await call(
            .signIn(
                .with {
                    $0.name = name
                    $0.password = password
                    $0.deviceName = deviceName
                })
        ).signIn.credentials
    }

    public func connect(deviceToken: String) async throws -> Wrinkle_V1_ConnectResponse {
        try await call(.connect(.with { $0.deviceToken = deviceToken })).connect
    }

    public func changePassword(current: String, new: String) async throws {
        _ = try await call(
            .changePassword(
                .with {
                    $0.currentPassword = current
                    $0.newPassword = new
                }))
    }

    /// Changes the given fields of the profile; nil keeps a field, an empty avatar removes it.
    public func updateProfile(name: String? = nil, icon: String? = nil, avatar: Data? = nil)
        async throws -> Wrinkle_V1_User
    {
        try await call(
            .updateProfile(
                .with {
                    if let name { $0.name = name }
                    if let icon { $0.icon = icon }
                    if let avatar { $0.avatar = avatar }
                })
        ).updateProfile.user
    }

    public func signOut() async throws {
        _ = try await call(.signOut(Wrinkle_V1_SignOutRequest()))
    }

    public func listDevices() async throws -> [Wrinkle_V1_Device] {
        try await call(.listDevices(Wrinkle_V1_ListDevicesRequest())).listDevices.devices
    }

    public func renameDevice(id: UInt64, name: String) async throws -> Wrinkle_V1_Device {
        try await call(
            .renameDevice(
                .with {
                    $0.deviceID = id
                    $0.name = name
                })
        ).renameDevice.device
    }

    /// Signs out another device of the account; its connections close with code 4001.
    public func signOutDevice(id: UInt64) async throws {
        _ = try await call(.signOutDevice(.with { $0.deviceID = id }))
    }

    public func deleteAccount(password: String) async throws {
        _ = try await call(.deleteAccount(.with { $0.password = password }))
    }

    /// A slot to PUT a blob to; see Blobs.upload.
    public func createUpload(purpose: Wrinkle_V1_BlobPurpose, mediaType: String, size: Int)
        async throws -> Wrinkle_V1_CreateUploadResponse
    {
        try await call(
            .createUpload(
                .with {
                    $0.purpose = purpose
                    $0.mediaType = mediaType
                    $0.size = UInt64(size)
                })
        ).createUpload
    }

    public func openConversation(peerName: String) async throws -> Wrinkle_V1_Conversation {
        try await call(.openConversation(.with { $0.peerName = peerName }))
            .openConversation.conversation
    }

    public func markRead(conversationID: Data, seq: UInt64) async throws {
        _ = try await call(
            .markRead(
                .with {
                    $0.conversationID = conversationID
                    $0.seq = seq
                }))
    }

    public func sync(cursor: UInt64, limit: UInt32) async throws -> Wrinkle_V1_SyncResponse {
        try await call(
            .sync(
                .with {
                    $0.cursor = cursor
                    $0.limit = limit
                })
        ).sync
    }

    public func registerPushToken(service: Wrinkle_V1_PushService, token: String) async throws {
        _ = try await call(
            .registerPushToken(
                .with {
                    $0.service = service
                    $0.token = token
                }))
    }
}

// MARK: Paged requests

extension Connection {
    /// All the user's conversations, the most recently active first, a page at a time; 0
    /// pages by the server's default.
    public func listConversations(pageSize: UInt32 = 0) async throws -> [Wrinkle_V1_Conversation] {
        var all: [Wrinkle_V1_Conversation] = []
        var token = Data()
        repeat {
            let page = try await call(
                .listConversations(
                    .with {
                        $0.pageToken = token
                        $0.limit = pageSize
                    })
            ).listConversations
            all += page.conversations
            token = page.nextPageToken
        } while !token.isEmpty
        return all
    }

    public func getMessages(conversationID: Data, beforeSeq: UInt64, limit: UInt32)
        async throws -> [Wrinkle_V1_Message]
    {
        try await getMessagesPage(
            conversationID: conversationID, beforeSeq: beforeSeq, limit: limit
        ).messages
    }

    /// A page of history, newest first, with whether older messages follow; a short page does
    /// not mean the end.
    public func getMessagesPage(conversationID: Data, beforeSeq: UInt64, limit: UInt32)
        async throws -> Wrinkle_V1_GetMessagesResponse
    {
        try await call(
            .getMessages(
                .with {
                    $0.conversationID = conversationID
                    $0.beforeSeq = beforeSeq
                    $0.limit = limit
                })
        ).getMessages
    }
}
