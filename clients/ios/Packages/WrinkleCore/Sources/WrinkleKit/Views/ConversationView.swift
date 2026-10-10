import SwiftData
import SwiftUI
import WrinkleCore

struct ConversationView: View {
    let conversationID: Int64
    @Environment(AppState.self) private var app
    @Query private var conversations: [StoredConversation]
    @Query private var messages: [StoredMessage]
    @State private var text = ""
    @State private var error: String?

    init(conversationID: Int64) {
        self.conversationID = conversationID
        let id = conversationID
        _conversations = Query(filter: #Predicate<StoredConversation> { $0.id == id })
        _messages = Query(
            filter: #Predicate<StoredMessage> { $0.conversationID == id },
            sort: \StoredMessage.seq)
    }

    private var conversation: StoredConversation? { conversations.first }
    private var me: Int64 { app.me.map { Int64($0.userID) } ?? 0 }

    var body: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 6) {
                        if let oldest = messages.first, oldest.seq > 1 {
                            Button("Load earlier") {
                                Task {
                                    try? await app.loadHistory(
                                        conversationID: conversationID, before: oldest.seq)
                                }
                            }
                            .font(.footnote)
                        }
                        ForEach(messages, id: \.seq) { m in
                            MessageBubble(
                                message: m, mine: m.senderID == me,
                                read: m.senderID == me && (conversation?.peerReadSeq ?? 0) >= m.seq
                            )
                            .id(m.seq)
                        }
                    }
                    .padding()
                }
                .onChange(of: messages.count) {
                    if let last = messages.last { proxy.scrollTo(last.seq, anchor: .bottom) }
                    app.markRead(conversationID: conversationID)
                }
            }
            Divider()
            HStack {
                TextField("Message", text: $text).textFieldStyle(.roundedBorder).onSubmit(send)
                Button("Send", action: send).disabled(
                    text.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            .padding()
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red).padding(.bottom)
            }
        }
        .navigationTitle(conversation?.peerName ?? "")
        .inlineTitle()
        .task {
            if messages.isEmpty {
                try? await app.loadHistory(conversationID: conversationID, before: nil)
            }
            app.markRead(conversationID: conversationID)
        }
    }

    private func send() {
        let t = text.trimmingCharacters(in: .whitespaces)
        guard !t.isEmpty else { return }
        text = ""
        Task {
            do {
                try await app.send(text: t, in: conversationID)
                error = nil
            } catch {
                self.error = "\(error)"
            }
        }
    }
}

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
