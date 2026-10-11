import SwiftData
import SwiftUI
import WrinkleCore

struct ConversationView: View {
    let conversationID: IDHex
    @Environment(AppState.self) private var app
    @Query private var conversations: [StoredConversation]
    @Query private var messages: [StoredMessage]
    @State private var text = ""
    @State private var error: String?

    init(conversationID: IDHex) {
        self.conversationID = conversationID
        let id = conversationID
        _conversations = Query(filter: #Predicate<StoredConversation> { $0.id == id })
        _messages = Query(
            filter: #Predicate<StoredMessage> { $0.conversationID == id },
            sort: \StoredMessage.seq)
    }

    private var conversation: StoredConversation? { conversations.first }
    private var trimmedText: String { text.trimmingCharacters(in: .whitespaces) }

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
                            let mine = m.senderID == app.myUserID
                            MessageBubble(
                                message: m, mine: mine,
                                read: mine && (conversation?.peerReadSeq ?? 0) >= m.seq
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
                Button("Send", action: send).disabled(trimmedText.isEmpty)
            }
            .padding()
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red).padding(.bottom)
            }
        }
        .navigationTitle(conversation?.peerDisplayName ?? "")
        .inlineTitle()
        .toolbar {
            ToolbarItem(placement: .principal) {
                if let conversation {
                    HStack(spacing: 8) {
                        Avatar(peerOf: conversation, size: 28)
                        Text(conversation.peerDisplayName).font(.headline)
                    }
                }
            }
        }
        .task {
            if messages.isEmpty {
                try? await app.loadHistory(conversationID: conversationID, before: nil)
            }
            app.markRead(conversationID: conversationID)
        }
    }

    private func send() {
        let t = trimmedText
        guard !t.isEmpty else { return }
        text = ""
        Task {
            do {
                try await app.send(text: t, in: conversationID)
                error = nil
            } catch {
                self.error = errorText(error)
            }
        }
    }
}
