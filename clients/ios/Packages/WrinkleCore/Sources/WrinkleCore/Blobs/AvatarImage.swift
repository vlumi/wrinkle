import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// Prepares a picture for upload as an avatar: the center square, at most 512 pixels a side,
/// as JPEG under the 512 KiB the contract allows.
public enum AvatarImage {
    public static let maxSide = 512
    public static let maxBytes = 512 * 1024
    public static let mediaType = "image/jpeg"

    private static let qualities: [Double] = [0.85, 0.7, 0.55, 0.4, 0.25]

    /// The centered square of a width × height image.
    public static func cropRect(width: Int, height: Int) -> CGRect {
        let side = min(width, height)
        return CGRect(x: (width - side) / 2, y: (height - side) / 2, width: side, height: side)
    }

    /// The side of the avatar made from a square of the given side: never enlarged.
    public static func outputSide(for side: Int, limit: Int = maxSide) -> Int {
        max(1, min(side, limit))
    }

    /// How large to decode an image with the given longer and shorter sides so that its
    /// shorter side still covers the output.
    static func decodeSize(longer: Int, shorter: Int, limit: Int = maxSide) -> Int {
        guard shorter > limit else { return longer }
        return min(longer, Int((Double(longer) * Double(limit) / Double(shorter)).rounded(.up)))
    }

    /// The avatar JPEG for a picture in any format ImageIO reads, or nil if it cannot be read.
    public static func jpeg(from data: Data) -> Data? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
            let props = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
            let w = props[kCGImagePropertyPixelWidth] as? Int,
            let h = props[kCGImagePropertyPixelHeight] as? Int, w > 0, h > 0
        else { return nil }
        // Decoded upright (the thumbnail applies the orientation) and no larger than needed.
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: decodeSize(longer: max(w, h), shorter: min(w, h)),
        ]
        guard let image = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary)
        else { return nil }
        var limit = maxSide
        while limit >= 64 {
            guard let square = squared(image, limit: limit) else { return nil }
            for quality in qualities {
                if let out = encode(square, quality: quality), out.count <= maxBytes { return out }
            }
            limit /= 2
        }
        return nil
    }

    /// The image's center square scaled to at most limit pixels a side.
    static func squared(_ image: CGImage, limit: Int) -> CGImage? {
        let crop = cropRect(width: image.width, height: image.height)
        guard let cropped = image.cropping(to: crop) else { return nil }
        let side = outputSide(for: cropped.width, limit: limit)
        guard
            let context = CGContext(
                data: nil, width: side, height: side, bitsPerComponent: 8, bytesPerRow: 0,
                space: CGColorSpace(name: CGColorSpace.sRGB) ?? CGColorSpaceCreateDeviceRGB(),
                bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)
        else { return nil }
        context.interpolationQuality = .high
        context.draw(cropped, in: CGRect(x: 0, y: 0, width: side, height: side))
        return context.makeImage()
    }

    static func encode(_ image: CGImage, quality: Double) -> Data? {
        let out = NSMutableData()
        guard
            let dest = CGImageDestinationCreateWithData(
                out as CFMutableData, UTType.jpeg.identifier as CFString, 1, nil)
        else { return nil }
        let options: [CFString: Any] = [kCGImageDestinationLossyCompressionQuality: quality]
        CGImageDestinationAddImage(dest, image, options as CFDictionary)
        guard CGImageDestinationFinalize(dest) else { return nil }
        return out as Data
    }
}

extension AvatarImage {
    /// Decodes an avatar for display.
    public static func decode(_ data: Data) -> CGImage? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        return CGImageSourceCreateImageAtIndex(source, 0, nil)
    }
}
