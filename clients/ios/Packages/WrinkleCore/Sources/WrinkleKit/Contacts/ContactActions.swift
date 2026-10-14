import SwiftUI
import WrinkleCore

/// A change to another user's contact state that is confirmed first.
enum ContactChange: Identifiable {
    case remove(Wrinkle_V1_User)
    case block(Wrinkle_V1_User)

    var id: String {
        switch self {
        case .remove(let u): "remove-" + u.userID.hex
        case .block(let u): "block-" + u.userID.hex
        }
    }

    var title: String {
        switch self {
        case .remove(let u): "Remove \(u.shownName) from your contacts?"
        case .block(let u): "Block \(u.shownName)?"
        }
    }

    var verb: String {
        switch self {
        case .remove: "Remove"
        case .block: "Block"
        }
    }

    var message: String {
        switch self {
        case .remove:
            "You can still message each other, but neither can add the other to a group."
        case .block:
            "They can no longer message you or ask to be your contact, and what they send in "
                + "groups is hidden."
        }
    }
}

extension Wrinkle_V1_User {
    /// The name shown for the user, "Someone" when not known.
    var shownName: String {
        name.isEmpty && !deleted ? "Someone" : UserDisplay.name(name, deleted: deleted)
    }
}

/// Words a failed contact request for the user.
func contactErrorText(_ error: Error) -> String {
    errorText(
        error,
        [
            .notFound: "No one by that name.",
            .invalidArgument: "That's you.",
            .permissionDenied: "Can't add this user.",
            .failedPrecondition: "Unblock this user first.",
        ])
}

/// The menu items for another user by how they stand to the user: add, accept, decline,
/// withdraw, remove, block or unblock. Removing and blocking are confirmed first.
struct ContactMenuItems: View {
    let user: Wrinkle_V1_User
    @Binding var confirming: ContactChange?
    let failed: (String) -> Void
    @Environment(AppState.self) private var app

    private var userID: IDHex { user.userID.hex }

    var body: some View {
        switch app.contactState(of: userID) {
        case .contact:
            Button("Remove contact", systemImage: "person.badge.minus") {
                confirming = .remove(user)
            }
            blockButton
        case .incoming:
            Button("Accept contact request", systemImage: "person.badge.plus") { add() }
            Button("Decline contact request", systemImage: "xmark") { remove() }
            blockButton
        case .outgoing:
            Button("Withdraw contact request", systemImage: "xmark") { remove() }
            blockButton
        case .blocked:
            Button("Unblock", systemImage: "hand.raised.slash") {
                run { try await app.unblockUser(userID) }
            }
        default:
            if !user.deleted {
                Button("Add contact", systemImage: "person.badge.plus") { add() }
            }
            blockButton
        }
    }

    private var blockButton: some View {
        Button("Block", systemImage: "hand.raised", role: .destructive) {
            confirming = .block(user)
        }
    }

    private func add() { run { try await app.addContact(userID) } }

    private func remove() { run { try await app.removeContact(userID) } }

    private func run(_ work: @escaping () async throws -> Void) {
        Task {
            do {
                try await work()
            } catch {
                failed(contactErrorText(error))
            }
        }
    }
}

/// Asks before removing a contact or blocking a user, then does it.
struct ContactChangeConfirmation: ViewModifier {
    @Binding var change: ContactChange?
    let failed: (String) -> Void
    @Environment(AppState.self) private var app

    func body(content: Content) -> some View {
        content.confirmationDialog(
            change?.title ?? "",
            isPresented: Binding(get: { change != nil }, set: { if !$0 { change = nil } }),
            titleVisibility: .visible,
            presenting: change
        ) { c in
            Button(c.verb, role: .destructive) { perform(c) }
            Button("Cancel", role: .cancel) {}
        } message: { c in
            Text(c.message)
        }
    }

    private func perform(_ c: ContactChange) {
        Task {
            do {
                switch c {
                case .remove(let u): try await app.removeContact(u.userID.hex)
                case .block(let u): try await app.blockUser(u)
                }
            } catch {
                failed(contactErrorText(error))
            }
        }
    }
}

extension View {
    func confirmingContactChange(
        _ change: Binding<ContactChange?>, failed: @escaping (String) -> Void
    ) -> some View {
        modifier(ContactChangeConfirmation(change: change, failed: failed))
    }
}
