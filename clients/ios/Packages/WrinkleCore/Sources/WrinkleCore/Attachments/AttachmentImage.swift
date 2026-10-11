import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// Prepares pictures for sending, since the services only ever hold ciphertext: scaled so the
/// longer side is at most 2048 pixels and re-encoded as JPEG (which also drops the metadata),
/// with a small JPEG thumbnail for the sealed content.
public enum AttachmentImage {
    public static let maxSide = 2048
    public static let thumbnailSide = 320
    public static let quality = 0.85
    public static let mediaType = "image/jpeg"
    static let minThumbnailSide = 40
    private static let thumbnailQualities: [Double] = [0.7, 0.5, 0.35, 0.2]

    /// An encoded image and its size in pixels.
    public struct Encoded: Sendable {
        public let data: Data
        public let width: Int
        public let height: Int
    }

    /// Whether the data is a GIF, which is sent as it is to keep its animation.
    public static func isGIF(_ data: Data) -> Bool {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
            let type = CGImageSourceGetType(source) as String?
        else { return false }
        return UTType(type)?.conforms(to: .gif) == true
    }

    /// The upright size in pixels of an image ImageIO reads.
    public static func size(of data: Data) -> (width: Int, height: Int)? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
            let props = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
            let w = props[kCGImagePropertyPixelWidth] as? Int,
            let h = props[kCGImagePropertyPixelHeight] as? Int, w > 0, h > 0
        else { return nil }
        // Orientations 5 to 8 turn the image a quarter.
        let orientation = props[kCGImagePropertyOrientation] as? Int ?? 1
        return orientation >= 5 ? (h, w) : (w, h)
    }

    /// The picture upright, scaled to at most maxSide, as JPEG; nil if ImageIO cannot read it.
    public static func jpeg(from data: Data) -> Encoded? {
        guard let image = decode(data, maxSide: maxSide), let out = encode(image, quality: quality)
        else { return nil }
        return Encoded(data: out, width: image.width, height: image.height)
    }

    /// A thumbnail of at most thumbnailSide pixels within the byte budget, smaller until it
    /// fits; nil if the image cannot be read or made that small.
    public static func thumbnail(from data: Data, maxBytes: Int) -> Data? {
        guard let image = decode(data, maxSide: thumbnailSide) else { return nil }
        var side = max(image.width, image.height)
        var current = image
        while side >= minThumbnailSide {
            for q in thumbnailQualities {
                if let out = encode(current, quality: q), out.count <= maxBytes { return out }
            }
            side = side * 3 / 4
            guard let smaller = scaled(image, longest: side) else { return nil }
            current = smaller
        }
        return nil
    }

    /// Decodes an image upright, no larger than maxSide pixels on its longer side.
    public static func decode(_ data: Data, maxSide: Int) -> CGImage? {
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
            let (w, h) = size(of: data)
        else { return nil }
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: min(maxSide, max(w, h)),
        ]
        return CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary)
    }

    static func scaled(_ image: CGImage, longest: Int) -> CGImage? {
        let ratio = Double(longest) / Double(max(image.width, image.height))
        let w = max(1, Int((Double(image.width) * ratio).rounded()))
        let h = max(1, Int((Double(image.height) * ratio).rounded()))
        return flattened(image, width: w, height: h)
    }

    /// The image drawn on white at the given size: JPEG has no transparency.
    static func flattened(_ image: CGImage, width: Int, height: Int) -> CGImage? {
        guard
            let context = CGContext(
                data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                space: CGColorSpace(name: CGColorSpace.sRGB) ?? CGColorSpaceCreateDeviceRGB(),
                bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)
        else { return nil }
        let rect = CGRect(x: 0, y: 0, width: width, height: height)
        context.setFillColor(red: 1, green: 1, blue: 1, alpha: 1)
        context.fill(rect)
        context.interpolationQuality = .high
        context.draw(image, in: rect)
        return context.makeImage()
    }

    static func encode(_ image: CGImage, quality: Double) -> Data? {
        let opaque: CGImage
        switch image.alphaInfo {
        case .none, .noneSkipFirst, .noneSkipLast:
            opaque = image
        default:
            guard let flat = flattened(image, width: image.width, height: image.height) else {
                return nil
            }
            opaque = flat
        }
        return AvatarImage.encode(opaque, quality: quality)
    }
}
