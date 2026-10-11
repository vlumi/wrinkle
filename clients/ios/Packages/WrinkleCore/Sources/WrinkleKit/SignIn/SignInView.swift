import SwiftData
import SwiftUI
import WrinkleCore

struct SignInView: View {
    @Environment(AppState.self) private var app
    @Environment(\.modelContext) private var context
    @State private var name = ""
    @State private var url = AppState.defaultURL

    private var trimmedName: String { name.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        NavigationStack {
            Form {
                Section("Sign in") {
                    TextField("Name", text: $name)
                        .plainTextEntry()
                    TextField("Gateway", text: $url)
                        .plainTextEntry()
                        .urlKeyboard()
                }
                Button("Sign in") {
                    app.signIn(name: trimmedName, url: url, context: context)
                }
                .disabled(trimmedName.isEmpty)
                if let error = app.lastError {
                    Text(error).foregroundStyle(.secondary).font(.footnote)
                }
            }
            .navigationTitle("Wrinkle")
        }
    }
}
