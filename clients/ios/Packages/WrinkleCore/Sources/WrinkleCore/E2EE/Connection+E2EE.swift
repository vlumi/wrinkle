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

    /// The conversation's key state and every key for the device after the epoch, a page at
    /// a time; 0 pages by the server's default.
    public func getConversationKeys(conversationID: Data, afterEpoch: UInt32, pageSize: UInt32 = 0)
        async throws -> Wrinkle_V1_GetConversationKeysResponse
    {
        var after = afterEpoch
        var all: Wrinkle_V1_GetConversationKeysResponse?
        while true {
            let page = try await call(
                .getConversationKeys(
                    .with {
                        $0.conversationID = conversationID
                        $0.afterEpoch = after
                        $0.limit = pageSize
                    })
            ).getConversationKeys
            if all == nil { all = page } else { all?.keys += page.keys }
            guard page.hasMore_p, let last = page.keys.last else {
                all?.hasMore_p = false
                return all ?? page
            }
            after = last.epoch
        }
    }

    /// Sends content already sealed, with the attachment blobs it refers to; see
    /// KeyManager.send.
    public func sendMessage(
        conversationID: Data, clientMessageID: String, sealed: Wrinkle_V1_Sealed,
        blobIDs: [Data] = []
    )
        async throws -> Wrinkle_V1_Message
    {
        try await call(
            .sendMessage(
                .with {
                    $0.conversationID = conversationID
                    $0.clientMessageID = clientMessageID
                    $0.sealed = sealed
                    $0.blobIds = blobIDs
                })
        ).sendMessage.message
    }
}
