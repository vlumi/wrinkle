import SwiftData
import SwiftUI
import WrinkleCore

struct ConversationView: View {
    let conversationID: IDHex
    @Environment(AppState.self) private var app
    @Query private var conversations: [StoredConversation]
    @Query private var messages: [StoredMessage]
    @Query private var members: [StoredMember]
    @Query private var verifications: [StoredVerification]
    @Query private var reactions: [StoredReaction]
    @State private var text = ""
    @State private var error: String?
    @State private var showInfo = false
    @State private var contact: Wrinkle_V1_User?
    /// The seq of the user's own message awaiting confirmation of its deletion.
    @State private var deleting: Int64?
    /// The seq of the message being reacted to with an emoji typed in.
    @State private var reactingWithOther: Int64?
    @State private var otherEmoji = ""
    @State private var showStickers = false

    init(conversationID: IDHex) {
        self.conversationID = conversationID
        let id = conversationID
        _conversations = Query(filter: #Predicate<StoredConversation> { $0.id == id })
        _messages = Query(
            filter: #Predicate<StoredMessage> { $0.conversationID == id },
            sort: \StoredMessage.seq)
        _members = Query(filter: #Predicate<StoredMember> { $0.conversationID == id })
        _reactions = Query(filter: #Predicate<StoredReaction> { $0.conversationID == id })
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
            if let changed = changedContacts.first {
                Button {
                    contact = changed
                } label: {
                    Label(
                        "The safety number with \(displayName(changed)) changed. Verify it again.",
                        systemImage: "exclamationmark.triangle.fill"
                    )
                    .font(.footnote)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(.plain)
                .foregroundStyle(.orange)
                .padding(.horizontal)
                .padding(.vertical, 8)
            }
            Divider()
            if conversation?.isFormer == true {
                Text("You are no longer in this group")
                    .font(.footnote).foregroundStyle(.secondary).padding()
            } else {
                HStack {
                    Button {
                        showStickers = true
                    } label: {
                        Image(systemName: "face.smiling")
                    }
                    .accessibilityLabel("Stickers")
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
            if let conversation {
                ToolbarItem(placement: Toolbar.trailing) {
                    Button {
                        if conversation.isGroup {
                            showInfo = true
                        } else {
                            contact = conversation.peer
                        }
                    } label: {
                        Image(systemName: "info.circle")
                    }
                    .accessibilityLabel(conversation.isGroup ? "Group info" : "Contact")
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
        .alert(
            "React with an emoji",
            isPresented: Binding(
                get: { reactingWithOther != nil }, set: { if !$0 { reactingWithOther = nil } })
        ) {
            TextField("Emoji", text: $otherEmoji)
            Button("React") {
                if let seq = reactingWithOther {
                    toggleReaction(otherEmoji.trimmingCharacters(in: .whitespaces), on: seq)
                }
                otherEmoji = ""
            }
            Button("Cancel", role: .cancel) { otherEmoji = "" }
        }
        .sheet(isPresented: $showStickers) {
            NavigationStack {
                StickerPicker(pick: send(sticker:)) { showStickers = false }
            }
        }
        .sheet(isPresented: $showInfo) {
            NavigationStack {
                GroupInfoView(conversationID: conversationID) { showInfo = false }
            }
        }
        .sheet(isPresented: Binding(get: { contact != nil }, set: { if !$0 { contact = nil } })) {
            if let contact {
                NavigationStack {
                    ContactView(user: contact)
                        .toolbar {
                            ToolbarItem(placement: .confirmationAction) {
                                Button("Done") { self.contact = nil }
                            }
                        }
                }
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
            if messages.contains(where: \.isLocked) || conversation?.lastSealed != nil {
                await app.unlock(conversationID: conversationID)
            }
            await app.loadFingerprints(
                contacts.map(\.userID.hex).filter { id in verifications.contains { $0.userID == id }
                })
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
                receipt: mine ? receipt(m) : nil,
                chips: ReactionRules.chips(reactions.filter { $0.seq == m.seq }, me: app.myUserID),
                names: reactorNames, toggle: { toggleReaction($0, on: m.seq) }
            )
            .contextMenu {
                if !m.isDeleted && conversation?.isFormer == false {
                    ControlGroup {
                        ForEach(ReactionRules.quick, id: \.self) { e in
                            Button(e) { toggleReaction(e, on: m.seq) }
                        }
                    }
                    .controlGroupStyle(.compactMenu)
                    Button("Other Emoji…", systemImage: "face.smiling") {
                        reactingWithOther = m.seq
                    }
                }
                if let sticker = Data.id(hex: m.stickerID), !m.isDeleted {
                    if app.isSaved(sticker) {
                        Button("Saved", systemImage: "checkmark") {}.disabled(true)
                    } else {
                        Button("Save to Palette", systemImage: "square.and.arrow.down") {
                            save(sticker)
                        }
                    }
                }
                if m.deletable(by: app.myUserID) {
                    Button("Delete for Everyone", systemImage: "trash", role: .destructive) {
                        deleting = m.seq
                    }
                }
            }
        }
    }
}

// MARK: People

extension ConversationView {
    /// The other people in the conversation.
    private var contacts: [Wrinkle_V1_User] {
        guard let c = conversation else { return [] }
        guard c.isGroup else { return [c.peer] }
        return members.filter { $0.current && $0.userID != app.myUserID }.map(\.user)
    }

    /// Verified contacts whose safety number has changed since.
    private var changedContacts: [Wrinkle_V1_User] {
        contacts.filter { u in
            let id = u.userID.hex
            let verified = verifications.first { $0.userID == id }?.fingerprint
            return verified != nil && app.verificationState(id, verified: verified) == .changed
        }
    }

    private func displayName(_ u: Wrinkle_V1_User) -> String {
        u.name.isEmpty && !u.deleted ? "someone" : UserDisplay.name(u.name, deleted: u.deleted)
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
}

// MARK: Actions

extension ConversationView {
    /// The names of users who reacted, as the members cache or the peer knows them.
    private func reactorNames(_ users: [IDHex]) -> [String] {
        users.map { id in
            if id == app.myUserID { return "You" }
            if let c = conversation, !c.isGroup, c.peerID == id { return c.peerDisplayName }
            if let row = members.first(where: { $0.userID == id }), row.known {
                return row.displayName
            }
            return "Someone"
        }
    }

    private func toggleReaction(_ emoji: String, on seq: Int64) {
        Task {
            do {
                try await app.toggleReaction(emoji, to: seq, in: conversationID)
                error = nil
            } catch {
                self.error = errorText(
                    error, [.permissionDenied: "You are no longer in this group."])
            }
        }
    }

    private func send(sticker: Data) {
        showStickers = false
        Task {
            do {
                try await app.send(sticker: sticker, in: conversationID)
                error = nil
            } catch {
                self.error = errorText(
                    error, [.permissionDenied: "You are no longer in this group."])
            }
        }
    }

    private func save(_ sticker: Data) {
        Task {
            do {
                try await app.saveSticker(sticker)
                error = nil
            } catch {
                self.error = errorText(
                    error,
                    [
                        .failedPrecondition: "Your palette is full; remove a sticker first.",
                        .notFound: "That sticker is not known.",
                    ])
            }
        }
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
