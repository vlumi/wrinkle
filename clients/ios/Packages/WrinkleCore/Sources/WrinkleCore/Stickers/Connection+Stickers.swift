import Foundation

// MARK: Stickers and the palette

extension Connection {
    /// Makes an uploaded sticker blob a sticker; see StickerImage and Blobs.upload.
    public func createSticker(blobID: Data) async throws -> Wrinkle_V1_Sticker {
        try await call(.createSticker(.with { $0.blobID = blobID })).createSticker.sticker
    }

    /// The known stickers among up to 100 ids, in the order asked.
    public func getStickers(ids: [Data]) async throws -> [Wrinkle_V1_Sticker] {
        try await call(.getStickers(.with { $0.stickerIds = ids })).getStickers.stickers
    }

    /// The user's palette, in their order.
    public func getPalette() async throws -> [Wrinkle_V1_Sticker] {
        try await call(.getPalette(Wrinkle_V1_GetPaletteRequest())).getPalette.stickers
    }

    public func saveSticker(id: Data) async throws {
        _ = try await call(.saveSticker(.with { $0.stickerID = id }))
    }

    public func removeSticker(id: Data) async throws {
        _ = try await call(.removeSticker(.with { $0.stickerID = id }))
    }

    /// Moves a saved sticker before another, or to the end when before is nil.
    public func moveSticker(id: Data, before: Data?) async throws {
        _ = try await call(
            .moveSticker(
                .with {
                    $0.stickerID = id
                    if let before { $0.beforeStickerID = before }
                }))
    }
}
