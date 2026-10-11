import Foundation

/// What a user's reactions to a message may be: up to three distinct emoji, each one grapheme
/// cluster of at most 32 bytes. The server cannot check, so clients do, on both ends.
public enum ReactionRules {
    public static let maxPerUser = 3
    public static let maxBytes = 32
    /// The quick choices offered for a message.
    public static let quick = ["👍", "❤️", "😂", "😮", "😢", "🙏"]

    /// Whether the text is a single emoji.
    public static func isEmoji(_ s: String) -> Bool {
        guard s.count == 1, s.utf8.count <= maxBytes else { return false }
        let scalars = s.unicodeScalars
        return scalars.contains { $0.properties.isEmojiPresentation }
            || (scalars.contains { $0.properties.isEmoji } && scalars.count > 1)
    }

    /// The valid emoji of a set as received: distinct, at most three, in order.
    public static func valid(_ emoji: [String]) -> [String] {
        var out: [String] = []
        for e in emoji where isEmoji(e) && !out.contains(e) && out.count < maxPerUser {
            out.append(e)
        }
        return out
    }

    /// The user's set after toggling the emoji: removed if in it, else added; nil when the set
    /// is full or the text is no emoji.
    public static func toggle(_ emoji: String, in own: [String]) -> [String]? {
        if own.contains(emoji) { return own.filter { $0 != emoji } }
        guard isEmoji(emoji), own.count < maxPerUser else { return nil }
        return own + [emoji]
    }

    /// One emoji under a message: how many reacted with it, who, and whether the user did.
    public struct Chip: Equatable, Sendable {
        public let emoji: String
        public let users: [IDHex]
        public let mine: Bool
        public var count: Int { users.count }
    }

    /// The chips of a message's reactions, in the order the emoji were first chosen.
    public static func chips(_ reactions: [StoredReaction], me: IDHex) -> [Chip] {
        var order: [String] = []
        var users: [String: [IDHex]] = [:]
        for r in reactions.sorted(by: { ($0.updatedAt, $0.userID) < ($1.updatedAt, $1.userID) }) {
            for e in r.emoji {
                if users[e] == nil { order.append(e) }
                users[e, default: []].append(r.userID)
            }
        }
        return order.map { e in
            let who = users[e] ?? []
            return Chip(emoji: e, users: who, mine: who.contains(me))
        }
    }
}
