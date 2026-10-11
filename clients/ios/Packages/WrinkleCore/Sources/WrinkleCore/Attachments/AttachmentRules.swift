import Foundation
import UniformTypeIdentifiers

/// A file chosen to send, as picked.
public struct OutgoingFile: Sendable, Identifiable {
    public let id: UUID
    public var data: Data
    public var name: String
    public var mediaType: String

    public init(data: Data, name: String, mediaType: String) {
        self.id = UUID()
        self.data = data
        self.name = name
        self.mediaType = mediaType
    }

    /// The media type for a file name's extension, or application/octet-stream.
    public static func mediaType(forName name: String) -> String {
        let ext = (name as NSString).pathExtension
        return UTType(filenameExtension: ext)?.preferredMIMEType ?? AttachmentRules.blobMediaType
    }

    public var isImage: Bool { mediaType.hasPrefix("image/") }
}

/// A file ready to encrypt: pictures scaled and re-encoded, with their size and thumbnail.
public struct PreparedFile: Sendable {
    public var data: Data
    public var name: String
    public var mediaType: String
    public var width = 0
    public var height = 0
    public var thumbnail = Data()
}

/// The limits of content.proto's attachments and how a device keeps within them.
public enum AttachmentRules {
    public static let maxItems = 10
    public static let maxCaption = 4000
    public static let maxBlobBytes = 25 * 1024 * 1024
    /// The largest file whose blob, with its nonce and tag, still fits.
    public static let maxFileBytes = maxBlobBytes - E2EE.nonceLength - 16
    public static let maxThumbnailBytes = 16 * 1024
    public static let blobMediaType = "application/octet-stream"
    /// The sealed content's ciphertext is at most 48 KiB with its tag.
    static let maxContentBytes = 48 * 1024 - 16
    /// Room for an attachment's fields besides its name and thumbnail.
    static let itemOverhead = 200
    static let minThumbnailBytes = 1024

    /// What each thumbnail may take so that count attachments with the caption and names fit
    /// in the sealed content.
    public static func thumbnailBudget(count: Int, caption: String, names: [String]) -> Int {
        guard count > 0 else { return maxThumbnailBytes }
        let fixed = caption.utf8.count + names.reduce(0) { $0 + $1.utf8.count }
        let room = (maxContentBytes - fixed - count * itemOverhead) / count
        return min(maxThumbnailBytes, max(0, room))
    }

    /// A picked file made ready to send: pictures other than GIFs scaled and re-encoded as
    /// JPEG, GIFs as they are; both with their size and a thumbnail within the budget. Other
    /// files go as they are.
    public static func prepare(_ file: OutgoingFile, thumbnailBytes: Int) -> PreparedFile {
        var out = PreparedFile(data: file.data, name: file.name, mediaType: file.mediaType)
        guard file.isImage, file.mediaType != "image/svg+xml" else { return out }
        if AttachmentImage.isGIF(file.data) {
            out.mediaType = "image/gif"
        } else if let jpeg = AttachmentImage.jpeg(from: file.data) {
            out.data = jpeg.data
            out.mediaType = AttachmentImage.mediaType
            out.name = jpegName(file.name)
        } else {
            return out
        }
        if let (w, h) = AttachmentImage.size(of: out.data) {
            out.width = w
            out.height = h
        }
        if thumbnailBytes >= minThumbnailBytes {
            out.thumbnail =
                AttachmentImage.thumbnail(from: out.data, maxBytes: thumbnailBytes) ?? Data()
        }
        return out
    }

    /// The name with a .jpg extension in place of its own.
    static func jpegName(_ name: String) -> String {
        let base = (name as NSString).deletingPathExtension
        return (base.isEmpty ? "photo" : base) + ".jpg"
    }

    /// The content for the attachments and caption. Thumbnails go, the largest first, if the
    /// whole would not fit; nil if it cannot fit even without them.
    public static func content(_ items: [Wrinkle_V1_Attachment], caption: String)
        -> Wrinkle_V1_Content?
    {
        var attachments = Wrinkle_V1_Attachments.with {
            $0.items = items
            $0.caption = caption
        }
        while true {
            let content = Wrinkle_V1_Content.with { $0.attachments = attachments }
            let size = (try? content.serializedBytes() as Data)?.count ?? .max
            if size <= maxContentBytes { return content }
            guard
                let largest = attachments.items.indices.max(by: {
                    attachments.items[$0].thumbnail.count < attachments.items[$1].thumbnail.count
                }), !attachments.items[largest].thumbnail.isEmpty
            else { return nil }
            attachments.items[largest].thumbnail = Data()
        }
    }

    /// Whether well-formed attachments arrived: 1 to 10 items, each with a 16-byte blob id,
    /// a 32-byte key and a 32-byte digest.
    static func valid(_ a: Wrinkle_V1_Attachments) -> Bool {
        (1...maxItems).contains(a.items.count)
            && a.items.allSatisfy {
                $0.blobID.count == Data.idLength && $0.key.count == E2EE.keyLength
                    && $0.digest.count == 32
            }
    }
}

extension Wrinkle_V1_Attachment: Identifiable {
    public var id: Data { blobID }

    /// A picture shown as one: an image with a size.
    public var isImage: Bool { mediaType.hasPrefix("image/") && width > 0 && height > 0 }
}

extension StoredMessage {
    public var attachmentContent: Wrinkle_V1_Attachments? {
        attachments.flatMap { try? Wrinkle_V1_Attachments(serializedBytes: $0) }
    }
}

extension Wrinkle_V1_Attachments {
    /// What the attachments read as in the list and in notifications: the caption, else
    /// "Photo", "n photos", "File: <name>" or "n files".
    public var preview: String {
        if !caption.isEmpty { return caption }
        let n = items.count
        if n > 0 && items.allSatisfy(\.isImage) { return n == 1 ? "Photo" : "\(n) photos" }
        if n == 1, let item = items.first { return "File: \(item.name)" }
        return "\(n) files"
    }
}
