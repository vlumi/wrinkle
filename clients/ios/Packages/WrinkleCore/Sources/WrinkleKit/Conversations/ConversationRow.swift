import SwiftUI
import WrinkleCore

struct ConversationRow: View {
    let conversation: StoredConversation
    let me: IDHex

    var body: some View {
        HStack {
            Avatar(name: conversation.peerName, icon: conversation.peerIcon)
            VStack(alignment: .leading) {
                Text(conversation.peerName).font(.headline)
                if let last = conversation.lastText {
                    Text((conversation.lastSenderID == me ? "You: " : "") + last)
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
}
