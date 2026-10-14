import SwiftUI
import WrinkleCore

/// Contacts to pick for a group, with a filter by name; the users given are left out.
struct ContactPickerSection: View {
    let title: String
    @Binding var picked: Set<IDHex>
    var excluding: Set<IDHex> = []
    @Environment(AppState.self) private var app
    @State private var filter = ""

    private var shown: [Wrinkle_V1_User] { app.contacts.pickable(filter, excluding: excluding) }

    var body: some View {
        Section(title) {
            TextField("Filter", text: $filter).plainTextEntry()
            if shown.isEmpty {
                Text(filter.isEmpty ? "No contacts to add" : "No matching contacts")
                    .foregroundStyle(.secondary)
            }
            ForEach(shown, id: \.userID) { u in
                let id = u.userID.hex
                Button {
                    if picked.contains(id) { picked.remove(id) } else { picked.insert(id) }
                } label: {
                    HStack {
                        Image(systemName: picked.contains(id) ? "checkmark.circle.fill" : "circle")
                            .foregroundStyle(.tint)
                        Avatar(user: u, size: 28)
                        Text(u.shownName)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(picked.contains(id) ? .isSelected : [])
            }
        }
    }
}

extension AppState {
    /// The names of picked contacts, as requests name members.
    func names(of picked: Set<IDHex>) -> [String] {
        contacts.users(.contact).filter { picked.contains($0.userID.hex) }.map(\.name)
    }
}
