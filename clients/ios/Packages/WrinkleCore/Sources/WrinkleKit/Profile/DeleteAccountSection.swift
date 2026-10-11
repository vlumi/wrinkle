import SwiftUI
import WrinkleCore

/// Deleting the account, after the password is entered again.
struct DeleteAccountSection: View {
    @Environment(AppState.self) private var app
    @State private var confirming = false
    @State private var password = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        Section {
            Button("Delete account…", role: .destructive) {
                password = ""
                confirming = true
            }
            .disabled(busy)
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red)
            }
        } footer: {
            Text("Your name becomes free for others and every device is signed out.")
        }
        .alert("Delete account?", isPresented: $confirming) {
            SecureField("Password", text: $password)
            Button("Delete", role: .destructive, action: delete)
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This cannot be undone. Enter your password to confirm.")
        }
    }

    private func delete() {
        let entered = password
        password = ""
        busy = true
        Task {
            do {
                try await app.deleteAccount(password: entered)
            } catch {
                self.error = errorText(error, [.unauthenticated: "The password is wrong."])
            }
            busy = false
        }
    }
}
