import SwiftData
import SwiftUI
import WrinkleCore

struct ConversationView: View {
    let conversationID: IDHex
    @Environment(AppState.self) private var app
    @Query private var conversations: [StoredConversation]
    @Query private var messages: [StoredMessage]
    @Query private var members: [StoredMember]
    @State private var text = ""
    @State private var error: String?
    @State private var showInfo = false
    /// The seq of the user's own message awaiting confirmation of its deletion.
    @State private var deleting: Int64?

    init(conversationID: IDHex) {
        self.conversationID = conversationID
        let id = conversationID
        _conversations = Query(filter: #Predicate<StoredConversation> { $0.id == id })
        _messages = Query(
            filter: #Predicate<StoredMessage> { $0.conversationID == id },
            sort: \StoredMessage.seq)
        _members = Query(filter: #Predicate<StoredMember> { $0.conversationID == id })
    }

    private var conversation: StoredConversation? { conversations.first }
    private var isGroup: Bool { conversation?.isGroup ?? false }
    private var trimmedText: String { text.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        VStack(spacing: 0) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 6) {
                        if let oldest = messages.first, oldest.seq > 1,
                            conversation?.historyComplete != true
                        {
                            Button("Load earlier") {
                                Task {
                                    try? await app.loadHistory(
                                        conversationID: conversationID, before: oldest.seq)
                                }
                            }
                            .font(.footnote)
                        }
                        ForEach(messages, id: \.seq) { m in
                            row(m).id(m.seq)
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
            if conversation?.isFormer == true {
                Text("You are no longer in this group")
                    .font(.footnote).foregroundStyle(.secondary).padding()
            } else {
                HStack {
                    TextField("Message", text: $text).textFieldStyle(.roundedBorder)
                        .onSubmit(send)
                    Button("Send", action: send).disabled(trimmedText.isEmpty)
                }
                .padding()
            }
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red).padding(.bottom)
            }
        }
        .navigationTitle(conversation?.title ?? "")
        .inlineTitle()
        .toolbar {
            ToolbarItem(placement: .principal) {
                if let conversation { header(conversation) }
            }
            if isGroup {
                ToolbarItem(placement: Toolbar.trailing) {
                    Button {
                        showInfo = true
                    } label: {
                        Image(systemName: "info.circle")
                    }
                    .accessibilityLabel("Group info")
                }
            }
        }
        .confirmationDialog(
            "Delete this message for everyone?",
            isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
            titleVisibility: .visible
        ) {
            Button("Delete for Everyone", role: .destructive) {
                if let seq = deleting { delete(seq) }
            }
        }
        .sheet(isPresented: $showInfo) {
            NavigationStack {
                GroupInfoView(conversationID: conversationID) { showInfo = false }
            }
        }
        // Again once connected: opened from a notification, the app is still connecting.
        .task(id: app.me?.userID) {
            if messages.isEmpty {
                try? await app.loadHistory(conversationID: conversationID, before: nil)
            }
            app.markRead(conversationID: conversationID)
            if conversation?.isGroup == true && conversation?.member == true {
                try? await app.loadMembers(conversationID: conversationID)
            }
        }
    }

    private func header(_ c: StoredConversation) -> some View {
        HStack(spacing: 8) {
            ConversationAvatar(conversation: c, size: 28)
            VStack(alignment: .leading, spacing: 0) {
                Text(c.title).font(.headline).lineLimit(1)
                if c.isGroup {
                    Text(c.memberCount == 1 ? "1 member" : "\(c.memberCount) members")
                        .font(.caption2).foregroundStyle(.secondary)
                }
            }
        }
    }

    @ViewBuilder
    private func row(_ m: StoredMessage) -> some View {
        if let notice = m.noticeContent {
            Text(app.sentence(notice, sender: m.senderID, in: conversationID))
                .font(.footnote).foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 4)
        } else {
            let mine = m.senderID == app.myUserID
            MessageBubble(
                message: m, mine: mine, sender: isGroup && !mine ? sender(m.senderID) : nil,
                receipt: mine ? receipt(m) : nil
            )
            .contextMenu {
                if m.deletable(by: app.myUserID) {
                    Button("Delete for Everyone", systemImage: "trash", role: .destructive) {
                        deleting = m.seq
                    }
                }
            }
        }
    }

    /// The sender as the members cache knows them; only the id if it does not.
    private func sender(_ id: IDHex) -> Wrinkle_V1_User {
        members.first { $0.userID == id && $0.known }?.user
            ?? .with { $0.userID = Data(hex: id) ?? Data() }
    }

    /// Direct: "read" once the peer has read it. Groups: "Read by n" on the user's latest
    /// message, n counting the other current members who have read it.
    private func receipt(_ m: StoredMessage) -> String? {
        guard let c = conversation else { return nil }
        guard c.isGroup else { return c.peerReadSeq >= m.seq ? "read" : nil }
        let latest = messages.last { $0.senderID == app.myUserID && $0.notice == nil }
        guard latest?.seq == m.seq else { return nil }
        let n = members.filter { $0.current && $0.userID != app.myUserID && $0.readSeq >= m.seq }
            .count
        return n > 0 ? "Read by \(n)" : nil
    }

    private func delete(_ seq: Int64) {
        deleting = nil
        Task {
            do {
                try await app.deleteMessage(seq, in: conversationID)
                error = nil
            } catch {
                self.error = errorText(error)
            }
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
                self.error = errorText(
                    error, [.permissionDenied: "You are no longer in this group."])
            }
        }
    }
}
