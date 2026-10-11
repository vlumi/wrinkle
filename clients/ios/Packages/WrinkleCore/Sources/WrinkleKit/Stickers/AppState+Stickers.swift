import CoreGraphics
import Foundation
import SwiftUI
import WrinkleCore

// MARK: Stickers and the palette

extension AppState {
    /// Sends a sticker: sealed content naming it, like any message.
    public func send(sticker: Data, in conversationID: IDHex) async throws {
        let (connection, store) = try connected()
        guard let keys, let c = store.conversation(conversationID) else {
            throw AccountError("Not connected.")
        }
        let m = try await keys.send(
            content: .with { $0.sticker = .with { $0.stickerID = sticker } },
            to: try wireID(conversationID), members: try members(of: c),
            clientMessageID: UUID().uuidString, connection)
        store.insert(m)
        if let c = store.conversation(conversationID), c.noteLatest(m) {
            c.readSeq = Int64(m.seq)
        }
        store.save()
    }

    public func isSaved(_ sticker: Data) -> Bool { palette.contains(sticker) }

    /// Saves a sticker at the end of the palette. The palette event that follows confirms it.
    public func saveSticker(_ sticker: Data) async throws {
        let (connection, _) = try connected()
        guard !isSaved(sticker) else { return }
        guard palette.count < Palette.maxStickers else {
            throw AccountError("Your palette is full; remove a sticker first.")
        }
        try await connection.saveSticker(id: sticker)
        if !isSaved(sticker) { palette.append(sticker) }
    }

    public func removeSticker(_ sticker: Data) async throws {
        let (connection, _) = try connected()
        try await connection.removeSticker(id: sticker)
        palette.removeAll { $0 == sticker }
    }

    /// Moves a sticker as SwiftUI's onMove describes it: shown at once, put back if refused.
    public func moveSticker(from source: Int, to destination: Int) async throws {
        let (connection, _) = try connected()
        guard let move = Palette.move(palette, from: source, to: destination) else { return }
        let previous = palette
        palette.move(fromOffsets: IndexSet(integer: source), toOffset: destination)
        let shown = palette
        do {
            try await connection.moveSticker(id: move.sticker, before: move.before)
        } catch {
            // Unless an event has replaced it meanwhile.
            if palette == shown { palette = previous }
            throw error
        }
    }

    /// Makes a sticker from a picture in any format ImageIO reads: cropped, scaled and encoded
    /// here, uploaded, then made a sticker, which the backend saves to the palette.
    public func createSticker(picture: Data) async throws {
        let (connection, _) = try connected()
        guard let base = blobBase else { throw AccountError("This server has no blob address.") }
        let png = await Task.detached { StickerImage.png(from: picture) }.value
        guard let png else { throw AccountError("That picture cannot be used.") }
        let slot = try await connection.createUpload(
            purpose: .sticker, mediaType: StickerImage.mediaType, size: png.count)
        do {
            try await Blobs.shared.upload(
                png, blob: slot.blobID, uploadPath: slot.uploadPath,
                mediaType: StickerImage.mediaType, base: base)
        } catch {
            throw AccountError("The picture could not be uploaded.")
        }
        let sticker = try await connection.createSticker(blobID: slot.blobID)
        if !isSaved(sticker.stickerID) && palette.count < Palette.maxStickers {
            palette.append(sticker.stickerID)
        }
    }

    /// A sticker decoded earlier, if it was.
    func stickerImage(_ sticker: IDHex) -> CGImage? { stickerImages[sticker].flatMap { $0 } }

    /// Whether the sticker was fetched but cannot be shown.
    func stickerFailed(_ sticker: IDHex) -> Bool {
        if case .some(.none) = stickerImages[sticker] { return true }
        return false
    }

    /// Fetches and decodes a sticker, from the disk cache when it is there.
    func loadSticker(_ sticker: IDHex) async {
        guard stickerImages[sticker] == nil, let id = Data.id(hex: sticker), let base = blobBase,
            let data = try? await Blobs.shared.data(blob: id, base: base)
        else { return }
        stickerImages[sticker] = StickerImage.decode(data)
    }
}
