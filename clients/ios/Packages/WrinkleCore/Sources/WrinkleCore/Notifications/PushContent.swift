import Foundation
import SwiftData
import os

/// What the notification service extension needs beyond the epoch keys to word a decrypted
/// push: which conversations are groups, the names of the people in them, and the users the
/// user blocked. The app writes it to the shared vault; the backend's push carries none of it.
public struct SharedNames: Codable, Equatable, Sendable {
    public var groups: Set<IDHex> = []
    public var names: [IDHex: String] = [:]
    public var blocked: Set<IDHex> = []

    public init(groups: Set<IDHex> = [], names: [IDHex: String] = [:], blocked: Set<IDHex> = []) {
        self.groups = groups
        self.names = names
        self.blocked = blocked
    }

    static let account = "names"

    public static func load(from vault: Vault) -> SharedNames {
        vault.get(account).flatMap { try? JSONDecoder().decode(SharedNames.self, from: $0) }
            ?? SharedNames()
    }

    public func save(to vault: Vault) {
        guard SharedNames.load(from: vault) != self,
            let data = try? JSONEncoder().encode(self)
        else { return }
        do {
            try vault.set(Self.account, data)
        } catch {
            Logger.keychain.error("Shared names not saved: \(error)")
        }
    }

    public static func forget(in vault: Vault) { vault.delete(account) }
}

/// The text of a message push, decrypted: the backend sends only "New message" and the sealed
/// content, which a held epoch key opens.
public enum PushContent {
    /// The notification body: the text, "Sticker" for a sticker, or the attachments' caption,
    /// "Photo", "n photos" or "File: <name>", in a direct conversation;
    /// "<sender>: <text>" in a group; nil when the push has no sealed content, no held key opens
    /// it, or its sender is someone the user blocked, so the generic body stays.
    public static func body(
        _ info: [AnyHashable: Any], keys: EpochKeys, names: SharedNames
    ) -> String? {
        guard let message = message(info), !names.blocked.contains(message.senderUserID.hex),
            let content = keys.open(message)
        else { return nil }
        let conversation = message.conversationID.hex
        guard names.groups.contains(conversation) else { return content.preview }
        let sender = names.names[message.senderUserID.hex] ?? "Someone"
        return "\(sender): \(content.preview)"
    }

    /// The message the push describes, with its sealed content.
    static func message(_ info: [AnyHashable: Any]) -> Wrinkle_V1_Message? {
        guard let conversation = (info["conversation_id"] as? String).flatMap(Data.id(hex:)),
            let sender = (info["sender_user_id"] as? String).flatMap(Data.id(hex:)),
            let clientMessageID = info["client_message_id"] as? String,
            let epoch = (info["epoch"] as? NSNumber)?.uint32Value,
            let nonce = (info["nonce"] as? String).flatMap({ Data(base64Encoded: $0) }),
            let ciphertext = (info["ciphertext"] as? String).flatMap({ Data(base64Encoded: $0) })
        else { return nil }
        return .with {
            $0.conversationID = conversation
            $0.senderUserID = sender
            $0.clientMessageID = clientMessageID
            $0.sealed = .with {
                $0.epoch = epoch
                $0.nonce = nonce
                $0.ciphertext = ciphertext
            }
        }
    }
}

// MARK: Shared names

extension Store {
    /// The groups and the names of the people in the user's conversations, for the extension.
    public func sharedNames() -> SharedNames {
        var shared = SharedNames()
        for c in (try? context.fetch(FetchDescriptor<StoredConversation>())) ?? [] {
            if c.isGroup {
                shared.groups.insert(c.id)
            } else if !c.peerID.isEmpty {
                shared.names[c.peerID] = c.peerDisplayName
            }
        }
        for m in (try? context.fetch(FetchDescriptor<StoredMember>())) ?? [] where m.known {
            shared.names[m.userID] = m.displayName
        }
        return shared
    }
}
