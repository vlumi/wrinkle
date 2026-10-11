import SwiftUI
import WrinkleCore

/// The palette as a grid to send from; its stickers are managed in PaletteView.
struct StickerPicker: View {
    @Environment(AppState.self) private var app
    let pick: (Data) -> Void
    let cancel: () -> Void

    private let columns = [GridItem(.adaptive(minimum: 80), spacing: 12)]

    var body: some View {
        Group {
            if app.palette.isEmpty {
                ContentUnavailableView {
                    Label("No stickers yet", systemImage: "face.smiling")
                } description: {
                    Text(
                        "Touch and hold a sticker someone sent you to save it, "
                            + "or make your own under Edit.")
                }
            } else {
                ScrollView {
                    LazyVGrid(columns: columns, spacing: 12) {
                        ForEach(app.palette, id: \.self) { id in
                            Button {
                                pick(id)
                            } label: {
                                StickerView(sticker: id.hex, size: 80)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    .padding()
                }
            }
        }
        .navigationTitle("Stickers")
        .inlineTitle()
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel", action: cancel)
            }
            ToolbarItem(placement: Toolbar.trailing) {
                NavigationLink("Edit") { PaletteView() }
            }
        }
    }
}
