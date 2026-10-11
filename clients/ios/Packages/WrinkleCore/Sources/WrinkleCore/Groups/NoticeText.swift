import Foundation

extension StoredMessage {
    public var noticeContent: Wrinkle_V1_Notice? {
        notice.flatMap { try? Wrinkle_V1_Notice(serializedBytes: $0) }
    }
}

/// Group notices as the sentences shown in the conversation and the list.
public enum NoticeText {
    /// The sentence for a notice. `sender` is the acting user's name as known, or nil; `me`
    /// the signed-in user, who is "you".
    public static func sentence(
        _ notice: Wrinkle_V1_Notice, senderID: IDHex, sender: String?, me: IDHex
    ) -> String {
        let actor = senderID == me ? "You" : (sender ?? "Someone")
        let name = { (u: Wrinkle_V1_User) in display(u, me: me) }
        switch notice.kind {
        case .groupCreated(let g):
            let with = g.members.isEmpty ? "" : " with " + list(g.members.map(name))
            return "\(actor) created the group\(with)"
        case .membersAdded(let a):
            return "\(actor) added \(list(a.users.map(name)))"
        case .memberRemoved(let r):
            return "\(actor) removed \(name(r.user))"
        case .memberLeft(let l):
            guard l.hasPromoted else { return "\(actor) left" }
            let promoted = name(l.promoted)
            let verb = l.promoted.userID.hex == me ? "are" : "is"
            return "\(actor) left; \(promoted) \(verb) now an admin"
        case .topicChanged(let t):
            return "\(actor) changed the topic to “\(t.topic)”"
        case .adminChanged(let a):
            return a.admin
                ? "\(actor) made \(name(a.user)) an admin"
                : "\(actor) removed \(name(a.user)) as an admin"
        case nil:
            return "\(actor) changed the group"
        }
    }

    /// A user named mid-sentence.
    static func display(_ u: Wrinkle_V1_User, me: IDHex) -> String {
        if u.userID.hex == me { return "you" }
        if u.name.isEmpty && !u.deleted { return "someone" }
        return UserDisplay.name(u.name, deleted: u.deleted)
    }

    /// "A", "A and B", "A, B and C".
    static func list(_ names: [String]) -> String {
        guard let last = names.last else { return "nobody" }
        guard names.count > 1 else { return last }
        return names.dropLast().joined(separator: ", ") + " and " + last
    }

    /// Whether the notice changes who is in the group or who is an admin.
    public static func changesMembers(_ notice: Wrinkle_V1_Notice) -> Bool {
        switch notice.kind {
        case .topicChanged, nil: false
        default: true
        }
    }
}
