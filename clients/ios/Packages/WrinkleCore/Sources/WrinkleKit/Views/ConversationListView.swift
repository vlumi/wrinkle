import SwiftData
import SwiftUI
import WrinkleCore

struct ConversationListView: View {
    @Environment(AppState.self) private var app
    @Query(sort: \StoredConversation.updatedAt, order: .reverse)
    private var conversations: [StoredConversation]
    @State private var peerName = ""
    @State private var path: [Int64] = []
    @State private var error: String?

    var body: some View {
        NavigationStack(path: $path) {
            List {
                Section {
                    HStack {
                        TextField("Start a conversation with…", text: $peerName)
                            .plainTextEntry()
                            .onSubmit(open)
                        Button("Open", action: open).disabled(peerName.isEmpty)
                    }
                    if let error {
                        Text(error).font(.footnote).foregroundStyle(.red)
                    }
                }
                Section {
                    ForEach(conversations) { c in
                        NavigationLink(value: c.id) {
                            ConversationRow(
                                conversation: c, me: app.me.map { Int64($0.userID) } ?? 0)
                        }
                    }
                }
            }
            .navigationTitle(app.me?.name ?? "Wrinkle")
            .navigationDestination(for: Int64.self) { id in
                ConversationView(conversationID: id)
            }
            .toolbar {
                ToolbarItem(placement: Toolbar.leading) {
                    StatusBadge(state: app.state)
                }
                ToolbarItem(placement: Toolbar.trailing) {
                    Button("Sign out") { app.signOut() }
                }
            }
        }
    }

    private func open() {
        let name = peerName.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty else { return }
        Task {
            do {
                let id = try await app.openConversation(peerName: name)
                peerName = ""
                error = nil
                path = [id]
            } catch {
                self.error = "\(error)"
            }
        }
    }
}

struct ConversationRow: View {
    let conversation: StoredConversation
    let me: Int64

    var body: some View {
        HStack {
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

struct StatusBadge: View {
    let state: ConnectionState

    var body: some View {
        let (text, color): (String, Color) =
            switch state {
            case .connected: ("online", .green)
            case .connecting: ("connecting", .orange)
            case .offline: ("offline", .red)
            }
        Label {
            Text(text).font(.caption).fixedSize()
        } icon: {
            Circle().fill(color).frame(width: 8, height: 8)
        }
        .labelStyle(.titleAndIcon)
    }
}
