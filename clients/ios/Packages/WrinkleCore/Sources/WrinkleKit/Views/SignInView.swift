import SwiftData
import SwiftUI
import WrinkleCore

struct SignInView: View {
    @Environment(AppState.self) private var app
    @Environment(\.modelContext) private var context
    @State private var name = ""
    @State private var url = AppState.defaultURL

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
                    app.signIn(
                        name: name.trimmingCharacters(in: .whitespaces), url: url, context: context)
                }
                .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty)
                if let error = app.lastError {
                    Text(error).foregroundStyle(.secondary).font(.footnote)
                }
            }
            .navigationTitle("Wrinkle")
        }
    }
}
