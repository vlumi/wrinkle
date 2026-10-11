import Foundation
import SwiftData

/// A conversation as the device last saw it. Ids are the lowercase hex of the 16-byte ids.
@Model
public final class StoredConversation {
    @Attribute(.unique) public var id: IDHex
    public var peerID: IDHex
    public var peerName: String
    public var peerIcon: String
    public var lastSeq: Int64
    public var lastSenderID: IDHex?
    public var lastText: String?
    public var lastSentAt: Date?
    public var readSeq: Int64
    public var peerReadSeq: Int64
    public var updatedAt: Date

    public init(id: IDHex, peer: Wrinkle_V1_User) {
        self.id = id
        self.peerID = peer.userID.hex
        self.peerName = peer.name
        self.peerIcon = peer.icon
        self.lastSeq = 0
        self.readSeq = 0
        self.peerReadSeq = 0
        self.updatedAt = Date()
    }

    public var unread: Int64 { max(0, lastSeq - readSeq) }

    public func apply(_ c: Wrinkle_V1_Conversation) {
        peerID = c.peer.userID.hex
        peerName = c.peer.name
        peerIcon = c.peer.icon
        lastSeq = max(lastSeq, Int64(c.lastSeq))
        if c.hasLastMessage { showLast(c.lastMessage) }
        readSeq = max(readSeq, Int64(c.readSeq))
        peerReadSeq = max(peerReadSeq, Int64(c.peerReadSeq))
    }

    /// Records a message as the latest if it is newer; returns whether it was.
    @discardableResult
    public func noteLatest(_ m: Wrinkle_V1_Message) -> Bool {
        guard Int64(m.seq) > lastSeq else { return false }
        lastSeq = Int64(m.seq)
        showLast(m)
        return true
    }

    private func showLast(_ m: Wrinkle_V1_Message) {
        lastSenderID = m.senderUserID.hex
        lastText = m.text
        lastSentAt = m.sentAt.date
        updatedAt = m.sentAt.date
    }
}

@Model
public final class StoredMessage {
    #Unique<StoredMessage>([\.conversationID, \.seq])
    public var conversationID: IDHex
    public var seq: Int64
    public var senderID: IDHex
    public var clientMessageID: String
    public var text: String
    public var sentAt: Date

    public init(_ m: Wrinkle_V1_Message) {
        conversationID = m.conversationID.hex
        seq = Int64(m.seq)
        senderID = m.senderUserID.hex
        clientMessageID = m.clientMessageID
        text = m.text
        sentAt = m.sentAt.date
    }
}

/// Writes contract objects into the store. Everything runs on the main actor with the app's
/// model context.
@MainActor
public struct Store {
    public let context: ModelContext

    public init(context: ModelContext) { self.context = context }

    public static let models: [any PersistentModel.Type] = [
        StoredConversation.self, StoredMessage.self,
    ]
    public static let schema = Schema(models)

    /// Bumped whenever the models change incompatibly: the device's store is only a cache of
    /// the backend, so an older one is discarded rather than migrated.
    static let version = 2

    /// The app's container. Stores of earlier versions are deleted first.
    public static func container() throws -> ModelContainer {
        let dir = URL.applicationSupportDirectory
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let current = "wrinkle-v\(version).store"
        let files = (try? FileManager.default.contentsOfDirectory(atPath: dir.path)) ?? []
        for name in files where isOldStore(name, current: current) {
            try? FileManager.default.removeItem(at: dir.appending(path: name))
        }
        let config = ModelConfiguration(schema: schema, url: dir.appending(path: current))
        return try ModelContainer(for: schema, configurations: config)
    }

    /// The default SwiftData store, which version 1 used, and earlier versioned ones, with
    /// their -shm and -wal companions.
    static func isOldStore(_ name: String, current: String) -> Bool {
        guard !name.hasPrefix(current) else { return false }
        return name.hasPrefix("default.store") || name.hasPrefix("wrinkle-v")
    }

    /// Forgets everything stored, as on signing out.
    public func reset() {
        try? context.delete(model: StoredMessage.self)
        try? context.delete(model: StoredConversation.self)
        save()
    }

    public func conversation(_ id: IDHex) -> StoredConversation? {
        var d = FetchDescriptor<StoredConversation>(predicate: #Predicate { $0.id == id })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }

    public func upsert(_ c: Wrinkle_V1_Conversation) -> StoredConversation {
        let id = c.conversationID.hex
        let stored =
            conversation(id)
            ?? {
                let s = StoredConversation(id: id, peer: c.peer)
                context.insert(s)
                return s
            }()
        stored.apply(c)
        return stored
    }

    public func insert(_ m: Wrinkle_V1_Message) {
        let id = m.conversationID.hex
        let seq = Int64(m.seq)
        var d = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == id && $0.seq == seq })
        d.fetchLimit = 1
        if (try? context.fetch(d).first) != nil { return }
        context.insert(StoredMessage(m))
    }

    public func oldestSeq(in conversationID: IDHex) -> Int64? {
        var d = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID },
            sortBy: [SortDescriptor(\.seq)])
        d.fetchLimit = 1
        return try? context.fetch(d).first?.seq
    }

    /// Shows a peer's new name and icon in every conversation with them.
    public func updatePeer(_ user: Wrinkle_V1_User) {
        let peer = user.userID.hex
        let d = FetchDescriptor<StoredConversation>(predicate: #Predicate { $0.peerID == peer })
        for c in (try? context.fetch(d)) ?? [] {
            c.peerName = user.name
            c.peerIcon = user.icon
        }
    }

    /// Applies one event from the user's log; returns the conversation it touched, if known.
    /// A profile event of the user themselves touches no conversation; the session shows it.
    public func apply(_ event: Wrinkle_V1_Event, me: IDHex) -> StoredConversation? {
        switch event.body {
        case .message(let e):
            let m = e.message
            insert(m)
            guard let c = conversation(m.conversationID.hex) else { return nil }
            c.noteLatest(m)
            if m.senderUserID.hex == me {
                c.readSeq = max(c.readSeq, Int64(m.seq))
            }
            return c
        case .read(let r):
            guard let c = conversation(r.conversationID.hex) else { return nil }
            if r.userID.hex == me {
                c.readSeq = max(c.readSeq, Int64(r.seq))
            } else {
                c.peerReadSeq = max(c.peerReadSeq, Int64(r.seq))
            }
            return c
        case .profile(let p):
            if p.user.userID.hex != me { updatePeer(p.user) }
            return nil
        default:
            return nil
        }
    }

    public func save() {
        try? context.save()
    }
}
