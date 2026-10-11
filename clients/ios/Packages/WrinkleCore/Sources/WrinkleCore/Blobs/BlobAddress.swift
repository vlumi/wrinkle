import Foundation

/// Where blobs are read and uploaded, derived from the gateway URL (see frame.proto): the same
/// host and port over https beside a wss endpoint, port 9200 over http beside a ws one, which
/// the contract allows only on the loopback interface.
public enum BlobAddress {
    static let plainPort = 9200

    /// The blob address for a gateway URL, or nil if it is not a ws or wss URL.
    public static func base(gateway: URL) -> URL? {
        guard var parts = URLComponents(url: gateway, resolvingAgainstBaseURL: false),
            parts.host?.isEmpty == false
        else { return nil }
        switch parts.scheme?.lowercased() {
        case "wss": parts.scheme = "https"
        case "ws":
            parts.scheme = "http"
            parts.port = plainPort
        default: return nil
        }
        parts.path = ""
        parts.query = nil
        parts.fragment = nil
        parts.user = nil
        parts.password = nil
        return parts.url
    }

    /// Where a blob is read: /blobs/<lowercase hex blob id> at the blob address.
    public static func url(blob: Data, base: URL) -> URL {
        base.appending(path: "blobs/\(blob.hex)")
    }

    /// Where an upload goes: CreateUploadResponse.upload_path (path and query) at the blob
    /// address. The query carries the signature, so it is kept byte for byte.
    public static func url(uploadPath: String, base: URL) -> URL? {
        guard uploadPath.hasPrefix("/blobs/") else { return nil }
        return URL(string: base.absoluteString + uploadPath)
    }
}
