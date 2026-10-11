import Foundation

@testable import WrinkleCore

/// Sealed content for the store tests: one epoch key that every test conversation holds.
enum TestSealing {
    static let epoch: UInt32 = 1
    static let key = Data(repeating: 0x42, count: 32)

    /// Epoch keys holding the test key for the conversations.
    static func keys(_ conversations: Data...) -> EpochKeys {
        let keys = EpochKeys(vault: MemoryVault())
        for id in conversations { keys.put(id, epoch: epoch, key: key) }
        return keys
    }
}

extension Wrinkle_V1_Message {
    /// Seals the text as the message's content, bound to its conversation, sender and client
    /// message id, which must be set first.
    mutating func seal(
        _ text: String, key: Data = TestSealing.key, epoch: UInt32 = TestSealing.epoch
    ) {
        seal(content: .with { $0.text = text }, key: key, epoch: epoch)
    }

    /// Seals the content, as seal(_:) does a text.
    mutating func seal(
        content: Wrinkle_V1_Content, key: Data = TestSealing.key,
        epoch: UInt32 = TestSealing.epoch
    ) {
        // A fixed key and a fixed nonce are fine in tests.
        // swiftlint:disable:next force_try
        sealed = try! E2EE.seal(
            content, key: key,
            context: E2EE.ContentContext(
                conversationID: conversationID, epoch: epoch, senderUserID: senderUserID,
                clientMessageID: clientMessageID),
            nonce: Data(repeating: 1, count: 12))
    }
}
