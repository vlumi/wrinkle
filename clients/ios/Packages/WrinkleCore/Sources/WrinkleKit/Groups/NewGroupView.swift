import SwiftUI
import WrinkleCore

/// Creating a group: a topic and the members to invite by name.
struct NewGroupView: View {
    @Environment(AppState.self) private var app
    @Environment(\.dismiss) private var dismiss
    let created: (IDHex) -> Void
    @State private var topic = ""
    @State private var names: [String] = []
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
            NameListSection(title: "Members", names: $names)
                .onChange(of: names) { clientGroupID = UUID().uuidString }
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
                    topic: trimmedTopic, memberNames: names, clientGroupID: clientGroupID)
                created(id)
            } catch {
                self.error = errorText(error, [.notFound: "No one by one of those names."])
            }
            busy = false
        }
    }
}

/// Names to invite, added one at a time; names may contain spaces.
struct NameListSection: View {
    let title: String
    @Binding var names: [String]
    @State private var entry = ""

    var body: some View {
        Section(title) {
            ForEach(names, id: \.self) { Text($0) }
                .onDelete { names.remove(atOffsets: $0) }
            HStack {
                TextField("Name", text: $entry).plainTextEntry().onSubmit(add)
                Button("Add", action: add).disabled(trimmed.isEmpty)
            }
        }
    }

    private var trimmed: String { entry.trimmingCharacters(in: .whitespaces) }

    private func add() {
        let name = trimmed
        guard !name.isEmpty else { return }
        if !names.contains(where: { $0.caseInsensitiveCompare(name) == .orderedSame }) {
            names.append(name)
        }
        entry = ""
    }
}
