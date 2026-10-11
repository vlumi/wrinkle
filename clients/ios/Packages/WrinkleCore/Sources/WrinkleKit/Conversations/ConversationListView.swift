import SwiftData
import SwiftUI
import UserNotifications
import WrinkleCore

struct ConversationListView: View {
    @Environment(AppState.self) private var app
    @Query(sort: \StoredConversation.updatedAt, order: .reverse)
    private var conversations: [StoredConversation]
    @State private var peerName = ""
    @State private var path: [IDHex] = []
    @State private var showProfile = false
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
                            ConversationRow(conversation: c, me: app.myUserID)
                        }
                    }
                }
            }
            .navigationTitle(app.me?.name ?? "Wrinkle")
            .navigationDestination(for: IDHex.self) { id in
                ConversationView(conversationID: id)
            }
            .onChange(of: app.pendingConversation, initial: true) { _, id in
                guard let id else { return }
                path = [id]
                app.pendingConversation = nil
            }
            .onChange(of: totalUnread, initial: true) { _, count in
                UNUserNotificationCenter.current().setBadgeCount(Int(count))
            }
            .toolbar {
                ToolbarItem(placement: Toolbar.leading) {
                    StatusBadge(state: app.state)
                }
                ToolbarItem(placement: Toolbar.trailing) {
                    Button {
                        showProfile = true
                    } label: {
                        Avatar(name: app.me?.name ?? "", icon: app.me?.icon ?? "", size: 30)
                    }
                    .accessibilityLabel("Profile")
                }
            }
            .sheet(isPresented: $showProfile) {
                NavigationStack {
                    ProfileView()
                        .toolbar {
                            ToolbarItem(placement: .confirmationAction) {
                                Button("Done") { showProfile = false }
                            }
                        }
                }
            }
        }
    }

    private var totalUnread: Int64 { conversations.reduce(0) { $0 + $1.unread } }

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
                self.error = errorText(error, [.notFound: "No one by that name."])
            }
        }
    }
}
