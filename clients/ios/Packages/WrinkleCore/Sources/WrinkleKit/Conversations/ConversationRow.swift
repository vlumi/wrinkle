import SwiftUI
import WrinkleCore

struct ConversationRow: View {
    @Environment(AppState.self) private var app
    let conversation: StoredConversation

    var body: some View {
        HStack {
            ConversationAvatar(conversation: conversation)
            VStack(alignment: .leading) {
                Text(conversation.title).font(.headline)
                if conversation.lastDeleted {
                    Text("Message removed")
                        .font(.subheadline).italic().foregroundStyle(.secondary).lineLimit(1)
                } else if let preview {
                    Text(preview)
                        .font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                } else {
                    Text("No messages yet").font(.subheadline).foregroundStyle(.secondary)
                }
            }
            Spacer()
            if conversation.unread > 0 {
                Text("\(conversation.unread)")
                    .font(.caption).padding(.horizontal, 8).padding(.vertical, 2)
                    .background(.tint, in: Capsule()).foregroundStyle(.white)
            }
        }
    }

    /// The latest message: a notice as its sentence; a text after its sender's name in a
    /// group, or "You" for the user's own.
    private var preview: String? {
        let c = conversation
        let sender = c.lastSenderID ?? ""
        if let data = c.lastNotice, let notice = try? Wrinkle_V1_Notice(serializedBytes: data) {
            return app.sentence(notice, sender: sender, in: c.id)
        }
        guard let text = c.lastText else { return nil }
        if sender == app.myUserID { return "You: " + text }
        if c.isGroup, let name = app.name(of: sender, in: c.id) { return "\(name): \(text)" }
        return text
    }
}
