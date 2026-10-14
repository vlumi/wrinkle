import SwiftUI
import WrinkleCore

/// The user's contacts: asking someone by name, the requests both ways, the contacts, who
/// open a direct conversation, and the blocked users.
struct ContactsView: View {
    @Environment(AppState.self) private var app
    /// Opens the direct conversation with a contact.
    let open: (IDHex) -> Void
    @State private var name = ""
    @State private var confirming: ContactChange?
    @State private var error: String?

    private var trimmedName: String { name.trimmingCharacters(in: .whitespaces) }
    private var incoming: [Wrinkle_V1_User] { app.contacts.users(.incoming) }
    private var outgoing: [Wrinkle_V1_User] { app.contacts.users(.outgoing) }
    private var contacts: [Wrinkle_V1_User] { app.contacts.users(.contact) }
    private var blocked: [Wrinkle_V1_User] { app.contacts.users(.blocked) }

    var body: some View {
        Form {
            Section {
                HStack {
                    TextField("Add contact", text: $name).plainTextEntry().onSubmit(add)
                    Button("Add", action: add).disabled(trimmedName.isEmpty)
                }
                if let error {
                    Text(error).font(.footnote).foregroundStyle(.red)
                }
            }
            if !incoming.isEmpty || !outgoing.isEmpty {
                Section("Requests") {
                    ForEach(incoming, id: \.userID) { incomingRow($0) }
                    ForEach(outgoing, id: \.userID) { outgoingRow($0) }
                }
            }
            Section("Contacts") {
                if contacts.isEmpty {
                    Text("No contacts yet").foregroundStyle(.secondary)
                }
                ForEach(contacts, id: \.userID) { contactRow($0) }
            }
            if !blocked.isEmpty {
                Section("Blocked") {
                    ForEach(blocked, id: \.userID) { blockedRow($0) }
                }
            }
        }
        .navigationTitle("Contacts")
        .inlineTitle()
        .confirmingContactChange($confirming) { error = $0 }
    }

    private func person(_ u: Wrinkle_V1_User) -> some View {
        HStack {
            Avatar(user: u, size: 32)
            Text(u.shownName)
        }
    }

    private func incomingRow(_ u: Wrinkle_V1_User) -> some View {
        HStack {
            person(u)
            Spacer()
            Button("Accept") { run { try await app.addContact(u.userID.hex) } }
                .buttonStyle(.borderedProminent)
            Button("Decline") { run { try await app.removeContact(u.userID.hex) } }
                .buttonStyle(.bordered)
        }
    }

    private func outgoingRow(_ u: Wrinkle_V1_User) -> some View {
        HStack {
            person(u)
            Spacer()
            Text("Requested").font(.caption).foregroundStyle(.secondary)
            Button("Withdraw") { run { try await app.removeContact(u.userID.hex) } }
                .buttonStyle(.bordered)
        }
    }

    private func contactRow(_ u: Wrinkle_V1_User) -> some View {
        HStack {
            Button {
                run { open(try await app.openConversation(with: u)) }
            } label: {
                person(u).frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            Menu {
                Button("Remove contact", systemImage: "person.badge.minus") {
                    confirming = .remove(u)
                }
                Button("Block", systemImage: "hand.raised", role: .destructive) {
                    confirming = .block(u)
                }
            } label: {
                Image(systemName: "ellipsis.circle")
            }
            .accessibilityLabel("Contact actions")
        }
    }

    private func blockedRow(_ u: Wrinkle_V1_User) -> some View {
        HStack {
            person(u)
            Spacer()
            Button("Unblock") { run { try await app.unblockUser(u.userID.hex) } }
                .buttonStyle(.bordered)
        }
    }

    private func add() {
        let n = trimmedName
        guard !n.isEmpty else { return }
        run {
            try await app.addContact(name: n)
            name = ""
        }
    }

    private func run(_ work: @escaping () async throws -> Void) {
        Task {
            do {
                try await work()
                error = nil
            } catch {
                self.error = contactErrorText(error)
            }
        }
    }
}
