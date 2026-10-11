import Foundation

/// How users are shown.
public enum UserDisplay {
    public static let deletedName = "Deleted account"

    /// The name to show for a user: a deleted account's name is empty and free for others.
    public static func name(_ name: String, deleted: Bool) -> String {
        deleted ? deletedName : name
    }
}

extension StoredConversation {
    /// The peer of a direct conversation, as last seen.
    public var peer: Wrinkle_V1_User {
        .with {
            $0.userID = Data(hex: peerID) ?? Data()
            $0.name = peerName
            $0.icon = peerIcon
            $0.avatar = Data(hex: peerAvatar) ?? Data()
            $0.deleted = peerDeleted
        }
    }
}
