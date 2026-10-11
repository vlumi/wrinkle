import SwiftUI
import WrinkleCore

struct MessageBubble: View {
    let message: StoredMessage
    let mine: Bool
    let read: Bool

    var body: some View {
        HStack {
            if mine { Spacer(minLength: 60) }
            VStack(alignment: .trailing, spacing: 2) {
                Text(message.text)
                Text(
                    message.sentAt.formatted(date: .omitted, time: .shortened)
                        + (read ? " · read" : "")
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
}
