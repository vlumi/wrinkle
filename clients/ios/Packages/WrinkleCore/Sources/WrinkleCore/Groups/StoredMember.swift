import Foundation
import SwiftData

/// A user as seen in a group: current members from GetMembers and notices, and former ones
/// kept so their messages still show a name.
@Model
public final class StoredMember {
    #Unique<StoredMember>([\.conversationID, \.userID])
    public var conversationID: IDHex
    public var userID: IDHex
    public var name: String
    public var icon: String
    /// The avatar blob id as hex; empty for none.
    public var avatar: String
    public var deleted: Bool
    public var current: Bool
    public var admin: Bool
    public var joinedSeq: Int64
    public var readSeq: Int64

    init(conversationID: IDHex, user: Wrinkle_V1_User) {
        self.conversationID = conversationID
        self.userID = user.userID.hex
        self.name = user.name
        self.icon = user.icon
        self.avatar = user.avatar.hex
        self.deleted = user.deleted
        self.current = false
        self.admin = false
        self.joinedSeq = 0
        self.readSeq = 0
    }

    public var displayName: String { UserDisplay.name(name, deleted: deleted) }

    /// A row made before the user's profile was seen has no name yet.
    public var known: Bool { deleted || !name.isEmpty }

    public var user: Wrinkle_V1_User {
        .with {
            $0.userID = Data(hex: userID) ?? Data()
            $0.name = name
            $0.icon = icon
            $0.avatar = Data(hex: avatar) ?? Data()
            $0.deleted = deleted
        }
    }

    func show(_ user: Wrinkle_V1_User) {
        name = user.name
        icon = user.icon
        avatar = user.avatar.hex
        deleted = user.deleted
    }
}

// MARK: Groups

extension Store {
    /// Everyone known in a group, current members first, those who joined first first.
    public func members(of conversationID: IDHex) -> [StoredMember] {
        let d = FetchDescriptor<StoredMember>(
            predicate: #Predicate { $0.conversationID == conversationID })
        return ((try? context.fetch(d)) ?? []).sorted(by: StoredMember.ordered)
    }

    public func member(_ userID: IDHex, in conversationID: IDHex) -> StoredMember? {
        var d = FetchDescriptor<StoredMember>(
            predicate: #Predicate { $0.conversationID == conversationID && $0.userID == userID })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }

    /// The row for a user in a group with their profile as given, made if new.
    @discardableResult
    func note(_ user: Wrinkle_V1_User, in conversationID: IDHex) -> StoredMember {
        if let row = member(user.userID.hex, in: conversationID) {
            row.show(user)
            return row
        }
        let row = StoredMember(conversationID: conversationID, user: user)
        context.insert(row)
        return row
    }

    /// The row for a user in a group, made without a profile if new.
    func row(for userID: Data, in conversationID: IDHex) -> StoredMember {
        if let row = member(userID.hex, in: conversationID) { return row }
        let row = StoredMember(conversationID: conversationID, user: .with { $0.userID = userID })
        context.insert(row)
        return row
    }

    /// Replaces the cached members with a GetMembers response; earlier members stay as former
    /// ones.
    public func replaceMembers(_ members: [Wrinkle_V1_Member], in conversationID: IDHex, me: IDHex)
    {
        for row in self.members(of: conversationID) { row.current = false }
        for m in members {
            let row = note(m.user, in: conversationID)
            row.current = true
            row.admin = m.admin
            row.joinedSeq = Int64(m.joinedSeq)
            row.readSeq = max(row.readSeq, Int64(m.readSeq))
        }
        guard let c = conversation(conversationID) else { return }
        c.memberCount = members.count
        if let mine = members.first(where: { $0.user.userID.hex == me }) {
            c.member = true
            c.admin = mine.admin
        }
    }

    /// Follows a newly stored notice in the members cache.
    func followMembers(_ notice: Wrinkle_V1_Notice, in m: Wrinkle_V1_Message) {
        let id = m.conversationID.hex
        let seq = Int64(m.seq)
        let sender = row(for: m.senderUserID, in: id)
        let join = { (u: Wrinkle_V1_User) in
            let row = self.note(u, in: id)
            row.current = true
            row.admin = false
            row.joinedSeq = seq
        }
        switch notice.kind {
        case .groupCreated(let g):
            sender.current = true
            sender.admin = true
            g.members.forEach(join)
        case .membersAdded(let a):
            a.users.forEach(join)
        case .memberRemoved(let r):
            let row = note(r.user, in: id)
            row.current = false
            row.admin = false
        case .memberLeft(let l):
            sender.current = false
            sender.admin = false
            if l.hasPromoted { note(l.promoted, in: id).admin = true }
        case .adminChanged(let a):
            note(a.user, in: id).admin = a.admin
        case .topicChanged, nil:
            break
        }
    }

    /// A user's name as this device knows it from a group or a direct conversation; nil if
    /// not known.
    public func name(of userID: IDHex, in conversationID: IDHex) -> String? {
        if let row = member(userID, in: conversationID), row.known { return row.displayName }
        var d = FetchDescriptor<StoredConversation>(predicate: #Predicate { $0.peerID == userID })
        d.fetchLimit = 1
        return (try? context.fetch(d).first)?.peerDisplayName
    }
}

extension StoredMember {
    /// Current members first, then by when they joined.
    static func ordered(_ a: StoredMember, _ b: StoredMember) -> Bool {
        if a.current != b.current { return a.current }
        return (a.joinedSeq, a.userID) < (b.joinedSeq, b.userID)
    }
}
