import Foundation

/// Attachments kept decrypted on disk by blob id, under Caches: downloaded, checked against
/// their digest and decrypted once; the sender's own kept as they were sent. Also encrypts
/// and uploads them.
public actor AttachmentFiles {
    public static let shared = AttachmentFiles(
        directory: URL.cachesDirectory.appending(
            path: "wrinkle-attachments", directoryHint: .isDirectory),
        blobs: .shared)

    static let uploadAttempts = 3

    private let directory: URL
    private let blobs: Blobs
    private var inFlight: [String: Task<Data, Error>] = [:]

    public init(directory: URL, blobs: Blobs) {
        self.directory = directory
        self.blobs = blobs
    }

    /// Where an attachment is kept: <blob id>/<its name>, so viewers and sharing see the name.
    public nonisolated func location(of a: Wrinkle_V1_Attachment) -> URL {
        directory.appending(path: a.blobID.hex, directoryHint: .isDirectory)
            .appending(path: Self.safeName(a.name))
    }

    /// The file's name made safe for the file system: no path separators, never hidden.
    static func safeName(_ name: String) -> String {
        var s = String(name.map { $0 == "/" || $0 == ":" || $0 == "\\" ? "_" : $0 }.prefix(120))
        while s.hasPrefix(".") { s.removeFirst() }
        return s.trimmingCharacters(in: .whitespaces).isEmpty ? "file" : s
    }

    /// The decrypted file on disk, downloaded, checked and decrypted unless kept already.
    public func file(_ a: Wrinkle_V1_Attachment, base: URL) async throws -> URL {
        let url = location(of: a)
        if FileManager.default.fileExists(atPath: url.path) { return url }
        _ = try await data(a, base: base)
        return url
    }

    /// The decrypted file's bytes; see file(_:base:).
    public func data(_ a: Wrinkle_V1_Attachment, base: URL) async throws -> Data {
        let url = location(of: a)
        if let kept = try? Data(contentsOf: url) { return kept }
        let key = a.blobID.hex
        if let running = inFlight[key] { return try await running.value }
        let blobs = self.blobs
        let task = Task {
            let blob = try await blobs.fetch(blob: a.blobID, base: base)
            return try E2EE.decryptBlob(blob, key: a.key, digest: a.digest)
        }
        inFlight[key] = task
        defer { inFlight[key] = nil }
        let plain = try await task.value
        keep(plain, at: url)
        return plain
    }

    private func keep(_ data: Data, at url: URL) {
        try? FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? data.write(to: url, options: .atomic)
    }

    /// Encrypts a prepared file under a new key, uploads the blob to a slot for an attachment
    /// (the PUT tried up to three times), keeps the file decrypted, and returns what the
    /// sealed content says about it.
    public func upload(
        _ file: PreparedFile, _ connection: Connection, base: URL,
        progress: (@Sendable (Double) -> Void)? = nil
    ) async throws -> Wrinkle_V1_Attachment {
        let sealed = try E2EE.encryptBlob(file.data)
        let slot = try await connection.createUpload(
            purpose: .attachment, mediaType: AttachmentRules.blobMediaType,
            size: sealed.blob.count)
        var attempt = 1
        while true {
            do {
                try await blobs.upload(
                    sealed.blob, blob: slot.blobID, uploadPath: slot.uploadPath,
                    mediaType: AttachmentRules.blobMediaType, base: base, cache: false,
                    progress: progress)
                break
            } catch {
                guard attempt < Self.uploadAttempts else { throw error }
                try await Task.sleep(for: .seconds(attempt))
                attempt += 1
            }
        }
        let attachment = Wrinkle_V1_Attachment.with {
            $0.blobID = slot.blobID
            $0.key = sealed.key
            $0.digest = sealed.digest
            $0.mediaType = file.mediaType
            $0.size = UInt64(file.data.count)
            $0.name = file.name
            $0.width = UInt32(file.width)
            $0.height = UInt32(file.height)
            $0.thumbnail = file.thumbnail
        }
        keep(file.data, at: location(of: attachment))
        return attachment
    }
}
