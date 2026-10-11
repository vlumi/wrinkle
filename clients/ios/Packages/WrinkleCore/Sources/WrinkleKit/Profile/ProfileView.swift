import SwiftUI
import WrinkleCore

/// The signed-in user's name, icon and password, and signing out.
struct ProfileView: View {
    @Environment(AppState.self) private var app
    @State private var name = ""
    @State private var nameStatus: Status?
    @State private var iconError: String?
    @State private var current = ""
    @State private var new = ""
    @State private var confirm = ""
    @State private var passwordStatus: Status?
    @State private var busy = false

    struct Status {
        let text: String
        let failed: Bool
    }

    private var me: Wrinkle_V1_User { app.me ?? Wrinkle_V1_User() }
    private var trimmedName: String { name.trimmingCharacters(in: .whitespaces) }
    private let columns = Array(repeating: GridItem(.flexible(), spacing: 8), count: 5)

    var body: some View {
        Form {
            Section {
                VStack(spacing: 8) {
                    Avatar(name: me.name, icon: me.icon, size: 72)
                    Text(me.name).font(.title2)
                }
                .frame(maxWidth: .infinity)
            }
            Section("Name") {
                TextField("Name", text: $name).plainTextEntry()
                Button("Save name", action: saveName)
                    .disabled(busy || trimmedName.isEmpty || trimmedName == me.name)
                StatusText(status: nameStatus)
            }
            Section("Icon") {
                LazyVGrid(columns: columns, spacing: 8) {
                    iconButton("")
                    ForEach(UserIcon.all, id: \.key) { iconButton($0.key) }
                }
                .padding(.vertical, 4)
                if let iconError {
                    Text(iconError).font(.footnote).foregroundStyle(.red)
                }
            }
            Section {
                SecureField("Current password", text: $current)
                SecureField("New password", text: $new)
                SecureField("Confirm new password", text: $confirm)
                Button("Change password", action: changePassword)
                    .disabled(busy || current.isEmpty || new.isEmpty)
                StatusText(status: passwordStatus)
            } header: {
                Text("Password")
            } footer: {
                Text("Changing the password signs out your other devices.")
            }
            Section {
                Button("Sign out", role: .destructive) {
                    busy = true
                    Task { await app.signOut() }
                }
                .disabled(busy)
            }
        }
        .navigationTitle("Profile")
        .onAppear { name = me.name }
        .onChange(of: me.name) { _, latest in name = latest }
    }

    private func iconButton(_ key: String) -> some View {
        Button {
            setIcon(key)
        } label: {
            Avatar(name: me.name, icon: key, size: 44)
                .overlay(Circle().stroke(Color.accentColor, lineWidth: me.icon == key ? 3 : 0))
        }
        .buttonStyle(.plain)
        .disabled(busy)
        .accessibilityLabel(key.isEmpty ? "No icon" : key)
    }

    private func saveName() {
        let name = trimmedName
        if let problem = AccountRules.nameProblem(name) {
            nameStatus = Status(text: problem, failed: true)
            return
        }
        run {
            do {
                try await app.updateProfile(name: name)
                nameStatus = Status(text: "Name changed.", failed: false)
            } catch {
                nameStatus = Status(
                    text: errorText(
                        error,
                        [
                            .alreadyExists: "That name is taken.",
                            .invalidArgument: "That name is not allowed.",
                        ]),
                    failed: true)
            }
        }
    }

    private func setIcon(_ key: String) {
        guard key != me.icon else { return }
        run {
            do {
                try await app.updateProfile(icon: key)
                iconError = nil
            } catch {
                iconError = errorText(error)
            }
        }
    }

    private func changePassword() {
        if let problem = AccountRules.passwordProblem(new) {
            passwordStatus = Status(text: problem, failed: true)
            return
        }
        guard new == confirm else {
            passwordStatus = Status(text: "The new passwords do not match.", failed: true)
            return
        }
        run {
            do {
                try await app.changePassword(current: current, new: new)
                current = ""
                new = ""
                confirm = ""
                passwordStatus = Status(
                    text: "Password changed. Your other devices were signed out.", failed: false)
            } catch {
                passwordStatus = Status(
                    text: errorText(error, [.unauthenticated: "The current password is wrong."]),
                    failed: true)
            }
        }
    }

    private func run(_ work: @escaping @MainActor () async -> Void) {
        busy = true
        Task {
            await work()
            busy = false
        }
    }
}

private struct StatusText: View {
    let status: ProfileView.Status?

    var body: some View {
        if let status {
            Text(status.text).font(.footnote).foregroundStyle(status.failed ? .red : .secondary)
        }
    }
}
