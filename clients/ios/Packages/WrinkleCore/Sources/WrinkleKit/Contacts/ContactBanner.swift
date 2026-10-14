import SwiftUI
import WrinkleCore

/// Above a direct conversation with someone not a contact: what stands between the two and
/// what the user can do about it. Nothing for a contact or a user the user blocked.
struct ContactBanner: View {
    let user: Wrinkle_V1_User
    @Environment(AppState.self) private var app
    @State private var confirming: ContactChange?
    @State private var error: String?

    private var userID: IDHex { user.userID.hex }

    var body: some View {
        let state = app.contactState(of: userID)
        if !user.deleted && state != .contact && state != .blocked {
            VStack(alignment: .leading, spacing: 6) {
                switch state {
                case .outgoing:
                    Text("Contact request sent")
                    HStack {
                        Button("Withdraw") { run { try await app.removeContact(userID) } }
                    }
                case .incoming:
                    Text("\(user.shownName) wants to add you as a contact")
                    HStack {
                        Button("Accept") { run { try await app.addContact(userID) } }
                            .buttonStyle(.borderedProminent)
                        Button("Decline") { run { try await app.removeContact(userID) } }
                        blockButton
                    }
                default:
                    Text("\(user.shownName) isn't in your contacts")
                    HStack {
                        Button("Add contact") { run { try await app.addContact(userID) } }
                            .buttonStyle(.borderedProminent)
                        blockButton
                    }
                }
                if let error {
                    Text(error).foregroundStyle(.red)
                }
            }
            .font(.footnote)
            .buttonStyle(.bordered)
            .controlSize(.small)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal)
            .padding(.vertical, 8)
            .background(.bar)
            .confirmingContactChange($confirming) { error = $0 }
        }
    }

    private var blockButton: some View {
        Button("Block", role: .destructive) { confirming = .block(user) }
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

/// In place of the composer of a direct conversation with a user the user blocked.
struct BlockedNotice: View {
    let user: Wrinkle_V1_User
    @Environment(AppState.self) private var app
    @State private var error: String?

    var body: some View {
        VStack(spacing: 6) {
            Text("You blocked \(user.shownName)").foregroundStyle(.secondary)
            Button("Unblock") {
                Task {
                    do {
                        try await app.unblockUser(user.userID.hex)
                        error = nil
                    } catch {
                        self.error = contactErrorText(error)
                    }
                }
            }
            .buttonStyle(.bordered)
            if let error {
                Text(error).foregroundStyle(.red)
            }
        }
        .font(.footnote)
        .frame(maxWidth: .infinity)
        .padding()
    }
}

/// A group message from a user the user blocked: collapsed until tapped.
struct HiddenMessage<Content: View>: View {
    @ViewBuilder let content: () -> Content
    @State private var shown = false

    var body: some View {
        if shown {
            content()
        } else {
            Button {
                shown = true
            } label: {
                Label("Message from a blocked user", systemImage: "hand.raised")
                    .font(.footnote).italic().foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .buttonStyle(.plain)
            .accessibilityHint("Shows the message")
        }
    }
}
