import SwiftUI
import WrinkleCore

/// Creating a group: a topic and the members, picked from the user's contacts.
struct NewGroupView: View {
    @Environment(AppState.self) private var app
    @Environment(\.dismiss) private var dismiss
    let created: (IDHex) -> Void
    @State private var topic = ""
    @State private var picked: Set<IDHex> = []
    /// One per group being made: kept across retries, so a lost response does not make a
    /// second group.
    @State private var clientGroupID = UUID().uuidString
    @State private var busy = false
    @State private var error: String?

    private var trimmedTopic: String { topic.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        Form {
            Section("Topic") {
                TextField("Topic", text: $topic)
                    .onChange(of: topic) { clientGroupID = UUID().uuidString }
            }
            ContactPickerSection(title: "Members", picked: $picked)
                .onChange(of: picked) { clientGroupID = UUID().uuidString }
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red)
            }
        }
        .navigationTitle("New group")
        .inlineTitle()
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Create", action: create).disabled(trimmedTopic.isEmpty || busy)
            }
        }
    }

    private func create() {
        guard trimmedTopic.utf8.count <= GroupRules.maxTopicBytes else {
            error = GroupRules.topicTooLong
            return
        }
        busy = true
        Task {
            do {
                let id = try await app.createGroup(
                    topic: trimmedTopic, memberNames: app.names(of: picked),
                    clientGroupID: clientGroupID)
                created(id)
            } catch {
                self.error = errorText(
                    error,
                    [
                        .notFound: "Someone picked no longer has an account.",
                        .failedPrecondition: "Someone picked is no longer your contact.",
                    ])
            }
            busy = false
        }
    }
}
