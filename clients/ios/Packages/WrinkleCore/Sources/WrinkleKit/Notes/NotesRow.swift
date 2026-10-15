import SwiftUI
import WrinkleCore

/// The notes' picture: a note glyph in a circle.
struct NotesAvatar: View {
    var size: CGFloat = 40

    var body: some View {
        ZStack {
            Circle().fill(Color.accentColor.opacity(0.15))
            Image(systemName: "note.text")
                .font(.system(size: size * 0.4))
                .foregroundStyle(.tint)
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// The pinned notes row: the notes conversation once there is one, else a row that opens it,
/// creating it on the server.
struct PinnedNotesRow: View {
    @Environment(AppState.self) private var app
    let notes: StoredConversation?
    let open: (IDHex) -> Void
    @State private var error: String?

    var body: some View {
        if let notes {
            NavigationLink(value: notes.id) {
                ConversationRow(conversation: notes)
            }
        } else {
            Button(action: create) {
                HStack {
                    NotesAvatar()
                    VStack(alignment: .leading) {
                        Text(StoredConversation.notesTitle).font(.headline)
                        Text(error ?? "Messages to yourself")
                            .font(.subheadline)
                            .foregroundStyle(error == nil ? Color.secondary : Color.red)
                            .lineLimit(1)
                    }
                    Spacer()
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
        }
    }

    private func create() {
        Task {
            do {
                open(try await app.openNotes())
                error = nil
            } catch {
                self.error = errorText(error)
            }
        }
    }
}
