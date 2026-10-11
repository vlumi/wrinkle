import Foundation
import os

/// The epoch keys this device holds, by conversation and epoch, in a vault the notification
/// service extension can read. One item per conversation: records of epoch (4 bytes,
/// big-endian) and key (32 bytes).
public struct EpochKeys: Sendable {
    let vault: Vault
    private let lock = OSAllocatedUnfairLock()
    private static let prefix = "epochs."

    public init(vault: Vault) { self.vault = vault }

    private static func account(_ conversationID: Data) -> String { prefix + conversationID.hex }

    /// Every key of a conversation, by epoch.
    public func all(_ conversationID: Data) -> [UInt32: Data] {
        Self.decode(vault.get(Self.account(conversationID)) ?? Data())
    }

    public func key(_ conversationID: Data, epoch: UInt32) -> Data? {
        all(conversationID)[epoch]
    }

    /// The highest epoch held; 0 for none.
    public func highest(_ conversationID: Data) -> UInt32 {
        all(conversationID).keys.max() ?? 0
    }

    public func put(_ conversationID: Data, epoch: UInt32, key: Data) {
        lock.withLock {
            var keys = all(conversationID)
            guard keys[epoch] != key else { return }
            keys[epoch] = key
            vault.set(Self.account(conversationID), Self.encode(keys))
        }
    }

    /// The conversations with keys held.
    public func conversations() -> [Data] {
        vault.accounts().compactMap {
            $0.hasPrefix(Self.prefix) ? Data.id(hex: String($0.dropFirst(Self.prefix.count))) : nil
        }
    }

    /// Forgets every epoch key, as on signing out.
    public func forgetAll() {
        lock.withLock {
            for id in conversations() { vault.delete(Self.account(id)) }
        }
    }

    /// Opens a message's sealed content with a held key; nil if the epoch's key is not held
    /// or the content does not open.
    public func open(_ m: Wrinkle_V1_Message) -> Wrinkle_V1_Content? {
        guard case .sealed(let sealed) = m.content,
            let key = key(m.conversationID, epoch: sealed.epoch)
        else { return nil }
        return try? E2EE.open(sealed, key: key, context: E2EE.ContentContext(m))
    }

    /// Opens a user's sealed reactions to a message with a held key; nil if the epoch's key
    /// is not held or they do not open.
    public func open(
        _ sealed: Wrinkle_V1_Sealed, reactionsOf userID: Data, to seq: UInt64,
        in conversationID: Data
    ) -> Wrinkle_V1_Reactions? {
        guard let key = key(conversationID, epoch: sealed.epoch) else { return nil }
        return try? E2EE.open(
            sealed, key: key,
            context: E2EE.ReactionContext(
                conversationID: conversationID, epoch: sealed.epoch, userID: userID, seq: seq))
    }

    static func encode(_ keys: [UInt32: Data]) -> Data {
        var d = Data()
        for epoch in keys.keys.sorted() {
            d.append(bigEndian: epoch)
            d.append(keys[epoch] ?? Data())
        }
        return d
    }

    static func decode(_ data: Data) -> [UInt32: Data] {
        let bytes = [UInt8](data)
        let size = 4 + E2EE.keyLength
        var keys: [UInt32: Data] = [:]
        var i = 0
        while i + size <= bytes.count {
            let epoch = bytes[i..<i + 4].reduce(UInt32(0)) { $0 << 8 | UInt32($1) }
            keys[epoch] = Data(bytes[i + 4..<i + size])
            i += size
        }
        return keys
    }
}
