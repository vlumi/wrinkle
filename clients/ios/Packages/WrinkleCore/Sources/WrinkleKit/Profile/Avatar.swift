import SwiftUI
import WrinkleCore

/// A user's picture in a circle; without one, their icon's emoji, or else the name's first
/// letter. A deleted account gets a neutral figure.
struct Avatar: View {
    @Environment(AppState.self) private var app
    let name: String
    let icon: String
    /// The avatar's blob id as hex; empty for none.
    let avatar: String
    let deleted: Bool
    let size: CGFloat

    init(name: String, icon: String, avatar: String = "", deleted: Bool = false, size: CGFloat = 40)
    {
        self.name = name
        self.icon = icon
        self.avatar = avatar
        self.deleted = deleted
        self.size = size
    }

    init(user: Wrinkle_V1_User, size: CGFloat = 40) {
        self.init(
            name: user.name, icon: user.icon, avatar: user.avatar.hex, deleted: user.deleted,
            size: size)
    }

    init(peerOf c: StoredConversation, size: CGFloat = 40) {
        self.init(
            name: c.peerName, icon: c.peerIcon, avatar: c.peerAvatar, deleted: c.peerDeleted,
            size: size)
    }

    var body: some View {
        ZStack {
            Circle().fill(deleted ? Color.gray.opacity(0.2) : Color.accentColor.opacity(0.15))
            if deleted {
                Image(systemName: "person.fill")
                    .font(.system(size: size * 0.5))
                    .foregroundStyle(.secondary)
            } else if let image = picture {
                Image(decorative: image, scale: 1).resizable().scaledToFill()
            } else if let emoji = UserIcon.emoji(for: icon) {
                Text(emoji).font(.system(size: size * 0.55))
            } else {
                Text(initial)
                    .font(.system(size: size * 0.45, weight: .semibold))
                    .foregroundStyle(.tint)
            }
        }
        .frame(width: size, height: size)
        .clipShape(Circle())
        .accessibilityHidden(true)
        .task(id: avatar) {
            if !deleted && !avatar.isEmpty { await app.loadAvatar(avatar) }
        }
    }

    private var picture: CGImage? { avatar.isEmpty ? nil : app.avatarImage(avatar) }

    private var initial: String { name.first.map { String($0).uppercased() } ?? "" }
}

/// A group's picture: a people glyph in a circle.
struct GroupAvatar: View {
    var size: CGFloat = 40

    var body: some View {
        ZStack {
            Circle().fill(Color.accentColor.opacity(0.15))
            Image(systemName: "person.3.fill")
                .font(.system(size: size * 0.35))
                .foregroundStyle(.tint)
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// A conversation's picture: the group's, the notes', or the peer's.
struct ConversationAvatar: View {
    let conversation: StoredConversation
    var size: CGFloat = 40

    var body: some View {
        if conversation.isGroup {
            GroupAvatar(size: size)
        } else if conversation.isNotes {
            NotesAvatar(size: size)
        } else {
            Avatar(peerOf: conversation, size: size)
        }
    }
}
