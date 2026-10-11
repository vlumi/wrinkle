import SwiftUI
import WrinkleCore

struct MessageBubble: View {
    let message: StoredMessage
    let mine: Bool
    /// Someone else's message in a group: who sent it.
    let sender: Wrinkle_V1_User?
    /// The read receipt under the user's own message, if any.
    let receipt: String?

    var body: some View {
        HStack(alignment: .bottom) {
            if mine { Spacer(minLength: 60) }
            if let sender { Avatar(user: sender, size: 28) }
            VStack(alignment: mine ? .trailing : .leading, spacing: 2) {
                if let sender {
                    Text(senderName(sender))
                        .font(.caption).bold().foregroundStyle(.tint)
                }
                if message.isDeleted {
                    Text("Message removed").italic().foregroundStyle(.secondary)
                } else {
                    Text(message.text)
                }
                Text(
                    message.sentAt.formatted(date: .omitted, time: .shortened)
                        + (receipt.map { " · " + $0 } ?? "")
                )
                .font(.caption2).foregroundStyle(.secondary)
            }
            .padding(10)
            .background(
                mine ? Color.green.opacity(0.2) : Color.gray.opacity(0.2),
                in: .rect(cornerRadius: 12))
            if !mine { Spacer(minLength: 60) }
        }
    }

    private func senderName(_ u: Wrinkle_V1_User) -> String {
        u.name.isEmpty && !u.deleted ? "Someone" : UserDisplay.name(u.name, deleted: u.deleted)
    }
}
