import SwiftData
import SwiftUI
import WrinkleCore

/// A group's topic and members: any member changes the topic and adds members; admins remove
/// members and make others admins; anyone may leave.
struct GroupInfoView: View {
    let conversationID: IDHex
    let done: () -> Void
    @Environment(AppState.self) private var app
    @Query private var conversations: [StoredConversation]
    @Query private var members: [StoredMember]
    @State private var topic = ""
    @State private var newNames: [String] = []
    @State private var confirmingLeave = false
    @State private var error: String?
    @State private var contact: Wrinkle_V1_User?

    init(conversationID: IDHex, done: @escaping () -> Void) {
        self.conversationID = conversationID
        self.done = done
        let id = conversationID
        _conversations = Query(filter: #Predicate<StoredConversation> { $0.id == id })
        _members = Query(
            filter: #Predicate<StoredMember> { $0.conversationID == id && $0.current },
            sort: [SortDescriptor(\StoredMember.joinedSeq), SortDescriptor(\StoredMember.userID)])
    }

    private var conversation: StoredConversation? { conversations.first }
    private var isMember: Bool { conversation?.member ?? false }
    private var isAdmin: Bool { conversation?.admin ?? false }
    private var trimmedTopic: String { topic.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        Form {
            if isMember {
                Section("Topic") {
                    HStack {
                        TextField("Topic", text: $topic)
                        Button("Save", action: saveTopic)
                            .disabled(trimmedTopic.isEmpty || trimmedTopic == conversation?.topic)
                    }
                }
                Section("\(members.count) members") {
                    ForEach(members, id: \.userID) { row($0) }
                }
                NameListSection(title: "Add members", names: $newNames)
                if !newNames.isEmpty {
                    Button("Add \(newNames.count == 1 ? "member" : "members")", action: add)
                }
                Section {
                    Button("Leave group", role: .destructive) { confirmingLeave = true }
                }
            } else {
                Text("You are no longer in this group").foregroundStyle(.secondary)
            }
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red)
            }
        }
        .navigationTitle(conversation?.topic ?? "Group")
        .inlineTitle()
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done", action: done)
            }
        }
        .confirmationDialog("Leave this group?", isPresented: $confirmingLeave) {
            Button("Leave", role: .destructive, action: leave)
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("You keep the messages so far but get no new ones.")
        }
        .task {
            topic = conversation?.topic ?? ""
            if isMember {
                await attempt { try await app.loadMembers(conversationID: conversationID) }
            }
        }
        .onChange(of: conversation?.topic) { _, new in topic = new ?? "" }
        .navigationDestination(item: $contact) { ContactView(user: $0) }
    }

    private func row(_ m: StoredMember) -> some View {
        let me = m.userID == app.myUserID
        return HStack {
            Avatar(user: m.user, size: 32)
            Text((m.known ? m.displayName : "Someone") + (me ? " (you)" : ""))
            Spacer()
            if m.admin {
                Text("Admin").font(.caption).foregroundStyle(.secondary)
            }
            if !me {
                Menu {
                    Button("Safety number", systemImage: "checkmark.shield") { contact = m.user }
                    if isAdmin {
                        Button(m.admin ? "Remove as admin" : "Make admin") {
                            run {
                                try await app.setAdmin(
                                    m.userID, admin: !m.admin, in: conversationID)
                            }
                        }
                        Button("Remove from group", role: .destructive) {
                            run { try await app.removeMember(m.userID, from: conversationID) }
                        }
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .accessibilityLabel("Member actions")
            }
        }
    }

    private func saveTopic() {
        let t = trimmedTopic
        guard t.utf8.count <= GroupRules.maxTopicBytes else {
            error = GroupRules.topicTooLong
            return
        }
        run { try await app.setTopic(t, of: conversationID) }
    }

    private func add() {
        let names = newNames
        run {
            try await app.addMembers(names, to: conversationID)
            newNames = []
        }
    }

    private func leave() {
        run {
            try await app.leaveGroup(conversationID)
            done()
        }
    }

    private func run(_ work: @escaping () async throws -> Void) {
        Task { await attempt(work) }
    }

    private func attempt(_ work: () async throws -> Void) async {
        do {
            try await work()
            error = nil
        } catch {
            self.error = errorText(
                error,
                [
                    .notFound: "No one by one of those names.",
                    .permissionDenied: "Only admins can do that.",
                    .failedPrecondition:
                        "Not possible: a group keeps an admin and has at most 256 members.",
                ])
        }
    }
}
