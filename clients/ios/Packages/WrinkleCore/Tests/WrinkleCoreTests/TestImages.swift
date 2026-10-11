import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

struct TestImageError: Error {}

/// A PNG of the given size; noise makes it hard to compress.
func testPNG(width: Int, height: Int, noise: Bool = false) throws -> Data {
    guard
        let context = CGContext(
            data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
            space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
    else { throw TestImageError() }
    context.setFillColor(red: 0.2, green: 0.4, blue: 0.8, alpha: 1)
    context.fill(CGRect(x: 0, y: 0, width: width, height: height))
    if noise, let pixels = context.data?.assumingMemoryBound(to: UInt32.self) {
        var seed: UInt32 = 12345
        for i in 0..<(context.bytesPerRow / 4 * height) {
            seed = seed &* 1_664_525 &+ 1_013_904_223
            pixels[i] = seed | 0xff00_0000
        }
    }
    let out = NSMutableData()
    guard let image = context.makeImage(),
        let dest = CGImageDestinationCreateWithData(
            out as CFMutableData, UTType.png.identifier as CFString, 1, nil)
    else { throw TestImageError() }
    CGImageDestinationAddImage(dest, image, nil)
    guard CGImageDestinationFinalize(dest) else { throw TestImageError() }
    return out as Data
}
