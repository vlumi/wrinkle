import Foundation
import WrinkleCore

/// Files being sent in one message: its client message id and those already uploaded, by the
/// picked file's id, so a retry neither uploads them again nor sends a second message.
struct OutgoingAttachments {
    var clientMessageID = UUID().uuidString
    var uploaded: [UUID: Wrinkle_V1_Attachment] = [:]
}

// MARK: Attachments

extension AppState {
    /// Sends files with a caption: each prepared here (pictures scaled, thumbnailed), encrypted
    /// under its own key and uploaded, then the sealed content describing them, listing their
    /// blobs. A failure keeps what was uploaded for the retry.
    public func send(files: [OutgoingFile], caption: String, in conversationID: IDHex)
        async throws
    {
        let (connection, store) = try connected()
        guard let keys, let c = store.conversation(conversationID) else {
            throw AccountError("Not connected.")
        }
        guard let base = blobBase else { throw AccountError("This server has no blob address.") }
        guard (1...AttachmentRules.maxItems).contains(files.count) else {
            throw AccountError("Send at most \(AttachmentRules.maxItems) files at a time.")
        }
        guard caption.count <= AttachmentRules.maxCaption else {
            throw AccountError("The caption is too long.")
        }
        let members = try members(of: c)
        var pending = outgoing[conversationID] ?? OutgoingAttachments()
        let budget = AttachmentRules.thumbnailBudget(
            count: files.count, caption: caption, names: files.map(\.name))
        var items: [Wrinkle_V1_Attachment] = []
        for file in files {
            if let done = pending.uploaded[file.id] {
                items.append(done)
                continue
            }
            let a = try await upload(file, thumbnailBytes: budget, connection, base: base)
            pending.uploaded[file.id] = a
            outgoing[conversationID] = pending
            items.append(a)
        }
        guard let content = AttachmentRules.content(items, caption: caption) else {
            throw AccountError("Too much for one message; send fewer files or a shorter caption.")
        }
        let m = try await keys.send(
            content: content, to: try wireID(conversationID), members: members,
            clientMessageID: pending.clientMessageID, blobIDs: items.map(\.blobID), connection)
        discardOutgoing(conversationID, files: files)
        store.insert(m)
        if let c = store.conversation(conversationID), c.noteLatest(m) {
            c.readSeq = Int64(m.seq)
        }
        store.save()
    }

    private func upload(
        _ file: OutgoingFile, thumbnailBytes: Int, _ connection: Connection, base: URL
    ) async throws -> Wrinkle_V1_Attachment {
        let id = file.id
        uploadProgress[id] = 0
        let prepared = await Task.detached {
            AttachmentRules.prepare(file, thumbnailBytes: thumbnailBytes)
        }.value
        guard prepared.data.count <= AttachmentRules.maxFileBytes else {
            throw AccountError("\(file.name) is too large to send.")
        }
        do {
            let a = try await AttachmentFiles.shared.upload(
                prepared, connection, base: base,
                progress: { [weak self] p in
                    Task { @MainActor in
                        if self?.uploadProgress[id] != nil { self?.uploadProgress[id] = p }
                    }
                })
            uploadProgress[id] = 1
            return a
        } catch let e as BackendError {
            uploadProgress[id] = nil
            throw e
        } catch {
            uploadProgress[id] = nil
            throw AccountError("\(file.name) could not be uploaded; try again.")
        }
    }

    /// Forgets the files being sent in the conversation, as when the user removes them all.
    public func discardOutgoing(_ conversationID: IDHex, files: [OutgoingFile] = []) {
        outgoing[conversationID] = nil
        for f in files { uploadProgress[f.id] = nil }
    }

    /// An attachment's file, decrypted on disk; downloaded and checked unless kept already.
    public func attachmentFile(_ a: Wrinkle_V1_Attachment) async throws -> URL {
        guard let base = blobBase else { throw AccountError("This server has no blob address.") }
        do {
            return try await AttachmentFiles.shared.file(a, base: base)
        } catch {
            throw AccountError("The attachment could not be downloaded.")
        }
    }
}
