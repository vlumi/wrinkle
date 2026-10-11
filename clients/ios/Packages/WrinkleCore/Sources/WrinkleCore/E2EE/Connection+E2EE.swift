import Foundation

// MARK: End-to-end encryption requests

extension Connection {
    public func setDeviceKey(_ publicKey: Data) async throws {
        _ = try await call(.setDeviceKey(.with { $0.publicKey = publicKey }))
    }

    /// The signed-in devices with keys of up to 256 users.
    public func getDeviceKeys(userIDs: [Data]) async throws -> [Wrinkle_V1_UserDevices] {
        try await call(.getDeviceKeys(.with { $0.userIds = userIDs })).getDeviceKeys.users
    }

    public func publishKeys(conversationID: Data, epoch: UInt32, keys: [Wrinkle_V1_WrappedKey])
        async throws
    {
        _ = try await call(
            .publishKeys(
                .with {
                    $0.conversationID = conversationID
                    $0.epoch = epoch
                    $0.keys = keys
                }))
    }

    public func getConversationKeys(conversationID: Data, afterEpoch: UInt32) async throws
        -> Wrinkle_V1_GetConversationKeysResponse
    {
        try await call(
            .getConversationKeys(
                .with {
                    $0.conversationID = conversationID
                    $0.afterEpoch = afterEpoch
                })
        ).getConversationKeys
    }

    /// Sends content already sealed; see KeyManager.send.
    public func sendMessage(
        conversationID: Data, clientMessageID: String, sealed: Wrinkle_V1_Sealed
    )
        async throws -> Wrinkle_V1_Message
    {
        try await call(
            .sendMessage(
                .with {
                    $0.conversationID = conversationID
                    $0.clientMessageID = clientMessageID
                    $0.sealed = sealed
                })
        ).sendMessage.message
    }
}
