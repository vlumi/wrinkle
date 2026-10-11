import PhotosUI
import SwiftUI
import WrinkleCore

/// The user's palette: making a sticker from a picture, reordering and removing.
struct PaletteView: View {
    @Environment(AppState.self) private var app
    @State private var item: PhotosPickerItem?
    @State private var busy = false
    @State private var error: String?

    private var full: Bool { app.palette.count >= Palette.maxStickers }

    var body: some View {
        List {
            Section {
                // The label closure is not main-actor isolated: it gets plain values.
                let working = busy
                PhotosPicker(selection: $item, matching: .images, photoLibrary: .shared()) {
                    HStack {
                        Label("New sticker", systemImage: "plus")
                        if working {
                            Spacer()
                            ProgressView()
                        }
                    }
                }
                .disabled(busy)
                if let error {
                    Text(error).font(.footnote).foregroundStyle(.red)
                }
            } footer: {
                Text(
                    full
                        ? "Your palette is full. A new sticker is made but not saved."
                        : "A picture's center square becomes a sticker anyone can see and save.")
            }
            Section {
                ForEach(app.palette, id: \.self) { id in
                    StickerView(sticker: id.hex, size: 56)
                }
                .onMove { from, to in
                    guard let source = from.first else { return }
                    run { try await app.moveSticker(from: source, to: to) }
                }
                .onDelete { offsets in
                    let ids = offsets.map { app.palette[$0] }
                    run { for id in ids { try await app.removeSticker(id) } }
                }
            } header: {
                Text("\(app.palette.count) of \(Palette.maxStickers)")
            }
        }
        .navigationTitle("My stickers")
        .toolbar {
            #if os(iOS)
            ToolbarItem(placement: Toolbar.trailing) { EditButton() }
            #endif
        }
        .onChange(of: item) { _, picked in
            guard let picked else { return }
            item = nil
            create(picked)
        }
    }

    private func create(_ picked: PhotosPickerItem) {
        run {
            guard let data = try await picked.loadTransferable(type: Data.self) else {
                throw AccountError("That picture cannot be used.")
            }
            try await app.createSticker(picture: data)
        }
    }

    private func run(_ work: @escaping @MainActor () async throws -> Void) {
        busy = true
        Task {
            do {
                try await work()
                error = nil
            } catch {
                self.error = errorText(error)
            }
            busy = false
        }
    }
}
