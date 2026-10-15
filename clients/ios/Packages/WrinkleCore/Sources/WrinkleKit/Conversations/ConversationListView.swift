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
    @State private var showNewGroup = false
    @State private var showContacts = false
    @State private var error: String?

    var body: some View {
        NavigationStack(path: $path) {
            List {
                if !app.securityNotices.isEmpty {
                    Section("Security") {
                        ForEach(app.securityNotices, id: \.self) { notice in
                            HStack(alignment: .top) {
                                Label(notice, systemImage: "lock.shield")
                                    .font(.footnote)
                                Spacer()
                                Button("Dismiss", systemImage: "xmark") {
                                    app.dismissSecurityNotice(notice)
                                }
                                .labelStyle(.iconOnly)
                                .buttonStyle(.borderless)
                            }
                        }
                    }
                }
                Section {
                    HStack {
                        TextField("Start a conversation with…", text: $peerName)
                            .plainTextEntry()
                            .onSubmit(open)
                        Button("Open", action: open).disabled(peerName.isEmpty)
                    }
                    ForEach(suggestions, id: \.userID) { u in
                        Button {
                            peerName = u.name
                            open()
                        } label: {
                            Label(u.shownName, systemImage: "person")
                        }
                    }
                    if let error {
                        Text(error).font(.footnote).foregroundStyle(.red)
                    }
                }
                Section {
                    PinnedNotesRow(notes: notes) { path = [$0] }
                    ForEach(conversations.filter { !$0.isNotes }) { c in
                        NavigationLink(value: c.id) {
                            ConversationRow(conversation: c)
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
                        showContacts = true
                    } label: {
                        contactsIcon
                    }
                    .accessibilityLabel("Contacts")
                    .accessibilityValue(
                        app.contacts.incomingCount > 0
                            ? "\(app.contacts.incomingCount) requests" : "")
                }
                ToolbarItem(placement: Toolbar.trailing) {
                    Button {
                        showNewGroup = true
                    } label: {
                        Image(systemName: "person.2.badge.plus")
                    }
                    .accessibilityLabel("New group")
                }
                ToolbarItem(placement: Toolbar.trailing) {
                    Button {
                        showProfile = true
                    } label: {
                        Avatar(user: app.me ?? Wrinkle_V1_User(), size: 30)
                    }
                    .accessibilityLabel("Profile")
                }
            }
            .sheet(isPresented: $showContacts) {
                NavigationStack {
                    ContactsView { id in
                        showContacts = false
                        path = [id]
                    }
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") { showContacts = false }
                        }
                    }
                }
            }
            .sheet(isPresented: $showNewGroup) {
                NavigationStack {
                    NewGroupView { id in
                        showNewGroup = false
                        path = [id]
                    }
                }
            }
            .alert(
                "New device",
                // The buttons answer; dismissing alone changes nothing.
                isPresented: Binding(get: { app.newDevice != nil }, set: { _ in }),
                presenting: app.newDevice
            ) { _ in
                Button("Share History") { app.decideSharing(true) }
                Button("Don't Share", role: .cancel) { app.decideSharing(false) }
            } message: { device in
                Text(
                    "\(device.name.isEmpty ? "A new device" : device.name) signed in. "
                        + "Share your message history with it?")
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

    /// The contacts button, with the number of requests awaiting an answer.
    private var contactsIcon: some View {
        Image(systemName: "person.crop.circle")
            .overlay(alignment: .topTrailing) {
                let n = app.contacts.incomingCount
                if n > 0 {
                    Text("\(n)")
                        .font(.caption2).bold().padding(.horizontal, 4)
                        .background(.red, in: Capsule()).foregroundStyle(.white)
                        .offset(x: 8, y: -6)
                }
            }
    }

    /// Contacts whose name contains what is typed, to open with a tap.
    private var suggestions: [Wrinkle_V1_User] {
        let typed = peerName.trimmingCharacters(in: .whitespaces)
        guard !typed.isEmpty else { return [] }
        return Array(
            app.contacts.pickable(typed)
                .filter { $0.name.caseInsensitiveCompare(typed) != .orderedSame }
                .prefix(3))
    }

    private var notes: StoredConversation? { conversations.first(where: \.isNotes) }

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
                self.error = errorText(
                    error,
                    [
                        .notFound: "No one by that name.",
                        .invalidArgument: "Write to yourself in Notes.",
                    ])
            }
        }
    }
}
