import Foundation
import SwiftData

// MARK: Sealed content

extension Store {
    /// Opens the message's sealed content if a held key does.
    public func open(_ stored: StoredMessage) {
        guard let data = stored.sealedMessage, let content = opened(data) else { return }
        stored.text = content.stickerID == nil ? content.text : ""
        stored.stickerID = content.stickerID?.hex ?? ""
        stored.sealedMessage = nil
    }

    /// Opens the latest message of the conversation, for the list: a sticker reads as
    /// "Sticker".
    public func open(_ c: StoredConversation) {
        guard let data = c.lastSealed, let content = opened(data) else { return }
        c.lastText = content.preview
        c.lastSealed = nil
    }

    private func opened(_ data: Data) -> Wrinkle_V1_Content? {
        guard let keys, let m = try? Wrinkle_V1_Message(serializedBytes: data) else { return nil }
        return keys.open(m)
    }

    /// The epochs that locked messages, list previews and reactions need, by conversation.
    public func locked() -> [IDHex: Set<UInt32>] {
        var need: [IDHex: Set<UInt32>] = [:]
        let note = { (data: Data) in
            guard let m = try? Wrinkle_V1_Message(serializedBytes: data) else { return }
            need[m.conversationID.hex, default: []].insert(m.sealed.epoch)
        }
        let messages = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.sealedMessage != nil })
        for m in (try? context.fetch(messages)) ?? [] { m.sealedMessage.map(note) }
        let conversations = FetchDescriptor<StoredConversation>(
            predicate: #Predicate { $0.lastSealed != nil })
        for c in (try? context.fetch(conversations)) ?? [] { c.lastSealed.map(note) }
        return need.merging(lockedReactions()) { $0.union($1) }
    }

    /// Tries the held keys on everything locked in the conversation.
    public func reopen(_ conversationID: IDHex) {
        let messages = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID && $0.sealedMessage != nil }
        )
        for m in (try? context.fetch(messages)) ?? [] { open(m) }
        if let c = conversation(conversationID) { open(c) }
        reopenReactions(conversationID)
    }
}
