import SwiftUI
import WrinkleCore

/// A sticker's image at a fixed size, fetched once and cached by id.
struct StickerView: View {
    @Environment(AppState.self) private var app
    /// The sticker id as hex.
    let sticker: IDHex
    var size: CGFloat = Palette.displaySize

    var body: some View {
        Group {
            if let image = app.stickerImage(sticker) {
                Image(decorative: image, scale: 1).resizable().scaledToFit()
            } else if app.stickerFailed(sticker) {
                Image(systemName: "questionmark.square.dashed")
                    .font(.system(size: size * 0.4)).foregroundStyle(.secondary)
            } else {
                ProgressView()
            }
        }
        .frame(width: size, height: size)
        .accessibilityLabel(Palette.previewText)
        .task(id: sticker) { await app.loadSticker(sticker) }
    }
}
