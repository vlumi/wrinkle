import Foundation

/// Reads blobs over HTTP and keeps them on disk by blob id: blobs never change, so a cached
/// copy is good for good. Also uploads them, with the PUT CreateUpload describes.
public actor Blobs {
    public static let shared = Blobs(
        directory: URL.cachesDirectory.appending(path: "wrinkle-blobs", directoryHint: .isDirectory)
    )

    private let directory: URL
    private let session: URLSession
    private var inFlight: [String: Task<Data, Error>] = [:]

    public init(directory: URL, session: URLSession = URLSession(configuration: .ephemeral)) {
        self.directory = directory
        self.session = session
    }

    /// A blob's bytes, from the disk cache or fetched from the blob address.
    public func data(blob: Data, base: URL) async throws -> Data {
        let key = blob.hex
        let file = directory.appending(path: key)
        if let cached = try? Data(contentsOf: file) { return cached }
        if let running = inFlight[key] { return try await running.value }
        let url = BlobAddress.url(blob: blob, base: base)
        let session = self.session
        let task = Task {
            let (data, response) = try await session.data(from: url)
            guard (response as? HTTPURLResponse)?.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            return data
        }
        inFlight[key] = task
        defer { inFlight[key] = nil }
        let data = try await task.value
        store(data, key: key)
        return data
    }

    /// Uploads bytes to an upload slot, with Content-Type and Content-Length exactly as the
    /// slot was requested, and caches them under the blob id.
    public func upload(
        _ data: Data, blob: Data, uploadPath: String, mediaType: String, base: URL
    ) async throws {
        guard let url = BlobAddress.url(uploadPath: uploadPath, base: base) else {
            throw URLError(.badURL)
        }
        var request = URLRequest(url: url)
        request.httpMethod = "PUT"
        request.setValue(mediaType, forHTTPHeaderField: "Content-Type")
        request.setValue(String(data.count), forHTTPHeaderField: "Content-Length")
        let (_, response) = try await session.upload(for: request, from: data)
        guard let status = (response as? HTTPURLResponse)?.statusCode, (200..<300).contains(status)
        else { throw URLError(.badServerResponse) }
        store(data, key: blob.hex)
    }

    private func store(_ data: Data, key: String) {
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try? data.write(to: directory.appending(path: key), options: .atomic)
    }
}
