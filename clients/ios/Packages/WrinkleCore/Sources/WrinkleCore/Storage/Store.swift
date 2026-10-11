import Foundation
import SwiftData

/// A conversation as the device last saw it. Ids are the lowercase hex of the 16-byte ids.
/// The peer fields are for direct conversations; topic, member count and the member and admin
/// flags for groups.
@Model
public final class StoredConversation {
    @Attribute(.unique) public var id: IDHex
    /// The ConversationKind's raw value.
    public var kind: Int
    public var peerID: IDHex
    public var peerName: String
    public var peerIcon: String
    /// The peer's avatar blob id as hex; empty for none.
    public var peerAvatar: String
    public var peerDeleted: Bool
    public var topic: String
    public var memberCount: Int
    /// Groups: the user is still a member; false once they left or were removed.
    public var member: Bool
    public var admin: Bool
    public var lastSeq: Int64
    public var lastSenderID: IDHex?
    public var lastText: String?
    /// The serialized latest message while its sealed content is not opened.
    public var lastSealed: Data?
    /// The serialized Notice when the latest message is one.
    public var lastNotice: Data?
    /// The latest message was deleted by its sender.
    public var lastDeleted: Bool
    public var lastSentAt: Date?
    public var readSeq: Int64
    public var peerReadSeq: Int64
    public var updatedAt: Date
    /// History has been fetched back to the first message the user may see.
    public var historyComplete: Bool

    public init(id: IDHex, peer: Wrinkle_V1_User) {
        self.id = id
        self.kind = Wrinkle_V1_ConversationKind.direct.rawValue
        self.peerID = peer.userID.hex
        self.peerName = peer.name
        self.peerIcon = peer.icon
        self.peerAvatar = peer.avatar.hex
        self.peerDeleted = peer.deleted
        self.topic = ""
        self.memberCount = 0
        self.member = true
        self.admin = false
        self.lastSeq = 0
        self.lastDeleted = false
        self.readSeq = 0
        self.peerReadSeq = 0
        self.updatedAt = Date()
        self.historyComplete = false
    }

    public var isGroup: Bool { kind == Wrinkle_V1_ConversationKind.group.rawValue }

    /// A group the user left or was removed from: its history stays, read-only.
    public var isFormer: Bool { isGroup && !member }

    /// The name shown for the conversation: the topic of a group, else the peer's name.
    public var title: String { isGroup ? topic : peerDisplayName }

    public var unread: Int64 { max(0, lastSeq - readSeq) }

    /// The peer's name as shown: a deleted account has none.
    public var peerDisplayName: String { UserDisplay.name(peerName, deleted: peerDeleted) }

    public func apply(_ c: Wrinkle_V1_Conversation) {
        if c.kind == .group {
            kind = Wrinkle_V1_ConversationKind.group.rawValue
        } else {
            peerID = c.peer.userID.hex
            showPeer(c.peer)
        }
        // Group state from a response older than what events have shown is stale.
        if Int64(c.lastSeq) >= lastSeq {
            topic = c.topic
            memberCount = Int(c.memberCount)
            member = c.member
            admin = c.admin
        }
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

    func showPeer(_ peer: Wrinkle_V1_User) {
        peerName = peer.name
        peerIcon = peer.icon
        peerAvatar = peer.avatar.hex
        peerDeleted = peer.deleted
    }

    /// Follows a group notice that is the conversation's latest message: the topic, the
    /// member count, and whether the user is a member and an admin.
    func follow(_ notice: Wrinkle_V1_Notice, sender: IDHex, me: IDHex) {
        switch notice.kind {
        case .groupCreated(let g):
            topic = g.topic
            memberCount = g.members.count + 1
            member = true
            admin = sender == me
        case .membersAdded(let a):
            joined(a.users, me: me)
        case .memberRemoved(let r):
            departed(r.user.userID.hex == me)
        case .memberLeft(let l):
            departed(sender == me)
            admin = admin || (l.hasPromoted && l.promoted.userID.hex == me)
        case .topicChanged(let t):
            topic = t.topic
        case .adminChanged(let a):
            admin = a.user.userID.hex == me ? a.admin : admin
        case nil:
            break
        }
    }

    private func joined(_ users: [Wrinkle_V1_User], me: IDHex) {
        memberCount += users.count
        if users.contains(where: { $0.userID.hex == me }) {
            member = true
            admin = false
        }
    }

    private func departed(_ isMe: Bool) {
        memberCount = max(0, memberCount - 1)
        if isMe {
            member = false
            admin = false
        }
    }

    /// The latest message was deleted: its text goes.
    func lastWasDeleted() {
        lastText = ""
        lastSealed = nil
        lastNotice = nil
        lastDeleted = true
    }

    private func showLast(_ m: Wrinkle_V1_Message) {
        lastSenderID = m.senderUserID.hex
        lastText = ""
        lastDeleted = m.hasDeletedAt
        lastNotice = nil
        lastSealed = nil
        switch m.content {
        case .notice(let n): lastNotice = try? n.serializedData()
        case .sealed: lastSealed = try? m.serializedData()
        case nil: break
        }
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
    /// The opened text or caption; empty for a notice, a sticker and while not opened.
    public var text: String
    /// The opened sticker's id as hex; empty for none.
    public var stickerID: IDHex
    /// The serialized Attachments of opened content that has them.
    public var attachments: Data?
    /// The serialized message while its sealed content is not opened.
    public var sealedMessage: Data?
    /// The serialized Notice, for a group notice.
    public var notice: Data?
    public var sentAt: Date
    /// When the sender deleted it; the text is then empty.
    public var deletedAt: Date?

    public init(_ m: Wrinkle_V1_Message) {
        conversationID = m.conversationID.hex
        seq = Int64(m.seq)
        senderID = m.senderUserID.hex
        clientMessageID = m.clientMessageID
        text = ""
        stickerID = ""
        switch m.content {
        case .notice(let n): notice = try? n.serializedData()
        case .sealed: sealedMessage = try? m.serializedData()
        case nil: break
        }
        sentAt = m.sentAt.date
        if m.hasDeletedAt { deletedAt = m.deletedAt.date }
    }

    public var isDeleted: Bool { deletedAt != nil }

    /// Sealed content this device cannot open (yet).
    public var isLocked: Bool { sealedMessage != nil }

    /// Whether the user may delete it: their own text, sticker or attachments, not yet deleted.
    public func deletable(by userID: IDHex) -> Bool {
        senderID == userID && notice == nil && deletedAt == nil
    }

    /// Forgets the content of a message its sender deleted.
    func delete(at date: Date) {
        guard deletedAt == nil else { return }
        text = ""
        stickerID = ""
        attachments = nil
        notice = nil
        sealedMessage = nil
        deletedAt = date
    }
}

/// Writes contract objects into the store. Everything runs on the main actor with the app's
/// model context.
@MainActor
public struct Store {
    public let context: ModelContext
    /// The epoch keys that open sealed content; without them content stays locked.
    public var keys: EpochKeys?

    public init(context: ModelContext, keys: EpochKeys? = nil) {
        self.context = context
        self.keys = keys
    }

    public static let models: [any PersistentModel.Type] = [
        StoredConversation.self, StoredMessage.self, StoredMember.self, StoredVerification.self,
        StoredReaction.self,
    ]
    public static let schema = Schema(models)

    /// Bumped whenever the models change incompatibly: the device's store is only a cache of
    /// the backend, so an older one is discarded rather than migrated.
    static let version = 9

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
        try? context.delete(model: StoredReaction.self)
        try? context.delete(model: StoredMessage.self)
        try? context.delete(model: StoredMember.self)
        try? context.delete(model: StoredConversation.self)
        try? context.delete(model: StoredVerification.self)
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
        open(stored)
        return stored
    }

    public func message(_ seq: Int64, in conversationID: IDHex) -> StoredMessage? {
        var d = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID && $0.seq == seq })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }

    /// Stores a message unless it is already; returns whether it was new. A stored message
    /// that has since been deleted loses its content.
    @discardableResult
    public func insert(_ m: Wrinkle_V1_Message) -> Bool {
        if let stored = message(Int64(m.seq), in: m.conversationID.hex) {
            if m.hasDeletedAt {
                stored.delete(at: m.deletedAt.date)
                dropReactions(to: stored.seq, in: stored.conversationID)
            }
            return false
        }
        let stored = StoredMessage(m)
        open(stored)
        context.insert(stored)
        return true
    }

    public func oldestSeq(in conversationID: IDHex) -> Int64? {
        var d = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID },
            sortBy: [SortDescriptor(\.seq)])
        d.fetchLimit = 1
        return try? context.fetch(d).first?.seq
    }

    /// Shows a peer's new profile, or their deleted account, in every conversation with them.
    public func updatePeer(_ user: Wrinkle_V1_User) {
        let peer = user.userID.hex
        let d = FetchDescriptor<StoredConversation>(predicate: #Predicate { $0.peerID == peer })
        for c in (try? context.fetch(d)) ?? [] { c.showPeer(user) }
        let m = FetchDescriptor<StoredMember>(predicate: #Predicate { $0.userID == peer })
        for row in (try? context.fetch(m)) ?? [] { row.show(user) }
    }

    /// Applies one event from the user's log; returns the conversation it touched, if known.
    /// A profile event of the user themselves touches no conversation; the session shows it.
    public func apply(_ event: Wrinkle_V1_Event, me: IDHex) -> StoredConversation? {
        switch event.body {
        case .message(let e):
            return apply(e.message, me: me)
        case .read(let r):
            return apply(r, me: me)
        case .messageDeleted(let d):
            return markDeleted(
                Int64(d.seq), in: d.conversationID.hex, at: d.deletedAt.date)
        case .profile(let p):
            if p.user.userID.hex != me { updatePeer(p.user) }
            return nil
        case .reaction(let r):
            return apply(r)
        default:
            return nil
        }
    }

    private func apply(_ m: Wrinkle_V1_Message, me: IDHex) -> StoredConversation? {
        let new = insert(m)
        if new, case .notice(let n) = m.content { followMembers(n, in: m) }
        guard let c = conversation(m.conversationID.hex) else { return nil }
        if c.noteLatest(m) {
            if case .notice(let n) = m.content { c.follow(n, sender: m.senderUserID.hex, me: me) }
            open(c)
        }
        if m.senderUserID.hex == me {
            c.readSeq = max(c.readSeq, Int64(m.seq))
        }
        return c
    }

    private func apply(_ r: Wrinkle_V1_ReadEvent, me: IDHex) -> StoredConversation? {
        guard let c = conversation(r.conversationID.hex) else { return nil }
        if r.userID.hex == me {
            c.readSeq = max(c.readSeq, Int64(r.seq))
        } else if c.isGroup {
            if let row = member(r.userID.hex, in: c.id) {
                row.readSeq = max(row.readSeq, Int64(r.seq))
            }
        } else {
            c.peerReadSeq = max(c.peerReadSeq, Int64(r.seq))
        }
        return c
    }

    public func save() {
        try? context.save()
    }
}
