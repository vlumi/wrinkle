import Foundation

/// User and conversation ids are 16 bytes on the wire. The device keys its store, navigation
/// and notifications by their lowercase hex, which is also what APNs payloads carry.
extension Data {
    public var hex: String {
        let digits = Array("0123456789abcdef")
        var out = ""
        out.reserveCapacity(count * 2)
        for byte in self {
            out.append(digits[Int(byte >> 4)])
            out.append(digits[Int(byte & 0x0f)])
        }
        return out
    }

    /// Parses hex of either case; nil for an odd length or a non-hex character.
    public init?(hex: String) {
        let chars = Array(hex.utf8)
        guard chars.count.isMultiple(of: 2) else { return nil }
        var bytes = [UInt8]()
        bytes.reserveCapacity(chars.count / 2)
        var i = 0
        while i < chars.count {
            guard let hi = Self.nibble(chars[i]), let lo = Self.nibble(chars[i + 1]) else {
                return nil
            }
            bytes.append(hi << 4 | lo)
            i += 2
        }
        self.init(bytes)
    }

    private static func nibble(_ c: UInt8) -> UInt8? {
        switch c {
        case UInt8(ascii: "0")...UInt8(ascii: "9"): c - UInt8(ascii: "0")
        case UInt8(ascii: "a")...UInt8(ascii: "f"): c - UInt8(ascii: "a") + 10
        case UInt8(ascii: "A")...UInt8(ascii: "F"): c - UInt8(ascii: "A") + 10
        default: nil
        }
    }
}

/// The hex of a 16-byte id, as the store, navigation and notifications key it.
public typealias IDHex = String

extension Data {
    public static let idLength = 16

    /// The id a hex string names, or nil unless it is the hex of exactly 16 bytes.
    public static func id(hex: IDHex) -> Data? {
        guard let d = Data(hex: hex), d.count == idLength else { return nil }
        return d
    }
}
