import Foundation
import SwiftData

/// A conversation as the device last saw it. Ids are within 63 bits, so Int64 holds them.
@Model
public final class StoredConversation {
    @Attribute(.unique) public var id: Int64
    public var peerID: Int64
    public var peerName: String
    public var lastSeq: Int64
    public var lastSenderID: Int64?
    public var lastText: String?
    public var lastSentAt: Date?
    public var readSeq: Int64
    public var peerReadSeq: Int64
    public var updatedAt: Date

    public init(id: Int64, peerID: Int64, peerName: String) {
        self.id = id
        self.peerID = peerID
        self.peerName = peerName
        self.lastSeq = 0
        self.readSeq = 0
        self.peerReadSeq = 0
        self.updatedAt = Date()
    }

    public var unread: Int64 { max(0, lastSeq - readSeq) }

    public func apply(_ c: Wrinkle_V1_Conversation) {
        peerID = Int64(c.peer.userID)
        peerName = c.peer.name
        lastSeq = max(lastSeq, Int64(c.lastSeq))
        if c.hasLastMessage {
            lastSenderID = Int64(c.lastMessage.senderUserID)
            lastText = c.lastMessage.text
            lastSentAt = c.lastMessage.sentAt.date
            updatedAt = c.lastMessage.sentAt.date
        }
        readSeq = max(readSeq, Int64(c.readSeq))
        peerReadSeq = max(peerReadSeq, Int64(c.peerReadSeq))
    }
}

@Model
public final class StoredMessage {
    #Unique<StoredMessage>([\.conversationID, \.seq])
    public var conversationID: Int64
    public var seq: Int64
    public var senderID: Int64
    public var clientMessageID: String
    public var text: String
    public var sentAt: Date

    public init(_ m: Wrinkle_V1_Message) {
        conversationID = Int64(m.conversationID)
        seq = Int64(m.seq)
        senderID = Int64(m.senderUserID)
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

    public static let schema = Schema([StoredConversation.self, StoredMessage.self])

    public func conversation(_ id: Int64) -> StoredConversation? {
        var d = FetchDescriptor<StoredConversation>(predicate: #Predicate { $0.id == id })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }

    public func upsert(_ c: Wrinkle_V1_Conversation) -> StoredConversation {
        let stored =
            conversation(Int64(c.conversationID))
            ?? {
                let s = StoredConversation(
                    id: Int64(c.conversationID), peerID: Int64(c.peer.userID), peerName: c.peer.name
                )
                context.insert(s)
                return s
            }()
        stored.apply(c)
        return stored
    }

    public func insert(_ m: Wrinkle_V1_Message) {
        let id = Int64(m.conversationID)
        let seq = Int64(m.seq)
        var d = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == id && $0.seq == seq })
        d.fetchLimit = 1
        if (try? context.fetch(d).first) != nil { return }
        context.insert(StoredMessage(m))
    }

    public func oldestSeq(in conversationID: Int64) -> Int64? {
        var d = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID },
            sortBy: [SortDescriptor(\.seq)])
        d.fetchLimit = 1
        return try? context.fetch(d).first?.seq
    }

    /// Applies one event from the user's log; returns the conversation it touched, if known.
    public func apply(_ event: Wrinkle_V1_Event, me: Int64) -> StoredConversation? {
        switch event.body {
        case .message(let e):
            let m = e.message
            insert(m)
            guard let c = conversation(Int64(m.conversationID)) else { return nil }
            if Int64(m.seq) > c.lastSeq {
                c.lastSeq = Int64(m.seq)
                c.lastSenderID = Int64(m.senderUserID)
                c.lastText = m.text
                c.lastSentAt = m.sentAt.date
                c.updatedAt = m.sentAt.date
            }
            return c
        case .read(let r):
            guard let c = conversation(Int64(r.conversationID)) else { return nil }
            if Int64(r.userID) == me {
                c.readSeq = max(c.readSeq, Int64(r.seq))
            } else {
                c.peerReadSeq = max(c.peerReadSeq, Int64(r.seq))
            }
            return c
        default:
            return nil
        }
    }

    public func save() {
        try? context.save()
    }
}
