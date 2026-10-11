import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// Prepares a picture for upload as a sticker: the center square, at most 512 pixels a side,
/// transparency kept, as PNG under the 256 KiB the contract allows, smaller until it fits.
public enum StickerImage {
    public static let maxSide = AvatarImage.maxSide
    public static let maxBytes = 256 * 1024
    public static let mediaType = "image/png"
    static let minSide = 64

    /// The sticker PNG for a picture in any format ImageIO reads, or nil if it cannot be read
    /// or made small enough.
    public static func png(from data: Data) -> Data? {
        guard let image = AvatarImage.upright(data) else { return nil }
        var limit = maxSide
        while limit >= minSide {
            guard let square = AvatarImage.squared(image, limit: limit, alpha: true) else {
                return nil
            }
            if let out = encode(square), out.count <= maxBytes { return out }
            limit = limit * 3 / 4
        }
        return nil
    }

    static func encode(_ image: CGImage) -> Data? {
        let out = NSMutableData()
        guard
            let dest = CGImageDestinationCreateWithData(
                out as CFMutableData, UTType.png.identifier as CFString, 1, nil)
        else { return nil }
        CGImageDestinationAddImage(dest, image, nil)
        guard CGImageDestinationFinalize(dest) else { return nil }
        return out as Data
    }

    /// Decodes a sticker for display: its first frame, no larger than maxSide, whatever the
    /// image claims.
    public static func decode(_ data: Data) -> CGImage? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: maxSide,
        ]
        return CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary)
    }
}
