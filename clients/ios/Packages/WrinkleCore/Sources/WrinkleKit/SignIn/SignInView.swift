import SwiftUI
import WrinkleCore

/// Signs in to an account or creates one, then the session connects with the device token.
struct SignInView: View {
    @Environment(AppState.self) private var app
    @State private var mode: AccountMode = .signIn
    @State private var url = ""
    @State private var name = ""
    @State private var password = ""
    @State private var confirm = ""
    @State private var error: String?
    @State private var busy = false

    private var trimmedName: String { name.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("Mode", selection: $mode) {
                        Text("Sign in").tag(AccountMode.signIn)
                        Text("Create account").tag(AccountMode.register)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                }
                Section {
                    TextField("Server", text: $url, prompt: Text(AppState.networkURLHint))
                        .plainTextEntry()
                        .urlKeyboard()
                } header: {
                    Text("Server")
                } footer: {
                    Text(
                        "\(AppState.defaultURL) from the simulator; "
                            + "\(AppState.networkURLHint) from a phone on the network.")
                }
                Section("Account") {
                    TextField("Name", text: $name).plainTextEntry()
                    SecureField("Password", text: $password)
                    if mode == .register {
                        SecureField("Confirm password", text: $confirm)
                    }
                }
                Section {
                    Button(action: submit) {
                        HStack {
                            Text(mode == .register ? "Create account" : "Sign in")
                            if busy {
                                Spacer()
                                ProgressView()
                            }
                        }
                    }
                    .disabled(busy || trimmedName.isEmpty || password.isEmpty)
                    if let error {
                        Text(error).font(.footnote).foregroundStyle(.red)
                    } else if let notice = app.notice {
                        Text(notice).font(.footnote).foregroundStyle(.secondary)
                    }
                }
            }
            .navigationTitle("Wrinkle")
            .onAppear { if url.isEmpty { url = app.lastURL } }
            .onChange(of: mode) { error = nil }
        }
    }

    private func submit() {
        let name = trimmedName
        if mode == .register {
            if let problem = AccountRules.nameProblem(name)
                ?? AccountRules.passwordProblem(password)
            {
                error = problem
                return
            }
            if password != confirm {
                error = "The passwords do not match."
                return
            }
        }
        error = nil
        busy = true
        let server = url.trimmingCharacters(in: .whitespaces)
        Task {
            defer { busy = false }
            do {
                try await app.authenticate(mode, name: name, password: password, url: server)
                password = ""
                confirm = ""
            } catch {
                self.error = errorText(
                    error,
                    [
                        .alreadyExists: "That name is taken.",
                        .unauthenticated: "Wrong name or password.",
                        .invalidArgument:
                            "Names are 1 to 64 bytes without leading or trailing spaces; "
                            + "passwords at least 8 bytes.",
                    ])
            }
        }
    }
}
