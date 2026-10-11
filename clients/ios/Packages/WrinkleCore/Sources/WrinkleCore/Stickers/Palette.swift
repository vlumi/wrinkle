import Foundation

/// The user's saved stickers, by id, in their order: at most 200.
public enum Palette {
    public static let maxStickers = 200
    /// How large a sticker is shown in a conversation, in points.
    public static let displaySize: Double = 144
    /// What a sticker message reads as in the list and in notifications.
    public static let previewText = "Sticker"

    /// The MoveSticker that moves the sticker at one offset to another, in the sense of
    /// SwiftUI's onMove: the destination is an offset in the list before the move. Nil when
    /// the order would not change.
    public static func move(_ ids: [Data], from source: Int, to destination: Int)
        -> (sticker: Data, before: Data?)?
    {
        guard ids.indices.contains(source), (0...ids.count).contains(destination),
            destination != source, destination != source + 1
        else { return nil }
        return (ids[source], destination < ids.count ? ids[destination] : nil)
    }
}

extension Wrinkle_V1_Content {
    /// The sticker's id when the content is a sticker with a well-formed id.
    public var stickerID: Data? {
        guard case .sticker(let s) = body, s.stickerID.count == Data.idLength else { return nil }
        return s.stickerID
    }

    /// The content as one line of text: the text, or "Sticker".
    public var preview: String {
        if case .sticker = body { return Palette.previewText }
        return text
    }
}

extension StoredMessage {
    public var isSticker: Bool { !stickerID.isEmpty }
}
