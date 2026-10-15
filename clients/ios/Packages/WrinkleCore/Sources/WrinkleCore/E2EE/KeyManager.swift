import CryptoKit
import Foundation

/// The device's end-to-end encryption state against one backend: its key pair, the epoch keys
/// it holds, the key directory of the users it talks to and each conversation's key roster.
/// Encrypts what it sends, starting a new epoch when the conversation's devices changed, and
/// fetches the keys of epochs it cannot yet open.
public actor KeyManager {
    public nonisolated let userID: Data
    public nonisolated let deviceID: UInt64
    public nonisolated let publicKey: Data
    public nonisolated let epochs: EpochKeys
    private let privateKey: Curve25519.KeyAgreement.PrivateKey
    /// Signed-in devices with keys, by user, as last fetched or told by device events.
    private var directory: [Data: [UInt64: Data]] = [:]
    /// Every device key seen, signed out or not, to check wraps from devices since gone.
    private var seen: [Data: [UInt64: Data]] = [:]
    private var rosters: [Data: Roster] = [:]

    public struct Recipient: Hashable, Sendable {
        public let userID: Data
        public let deviceID: UInt64
    }

    struct Roster {
        var epoch: UInt32
        var rotate: Bool
        var recipients: Set<Recipient>
        var members: [Data]
    }

    public static let maxKeysPerPublish = 500
    static let maxUsersPerLookup = 256

    public init(
        userID: Data, deviceID: UInt64, privateKey: Curve25519.KeyAgreement.PrivateKey,
        epochs: EpochKeys
    ) {
        self.userID = userID
        self.deviceID = deviceID
        self.privateKey = privateKey
        self.publicKey = privateKey.publicKey.rawRepresentation
        self.epochs = epochs
    }

    /// Publishes this device's public key; FAILED_PRECONDITION means the backend has another
    /// one for this device, whose private key is lost.
    public func publish(_ connection: Connection) async throws {
        try await connection.setDeviceKey(publicKey)
    }

    // MARK: Directory

    /// The users' signed-in devices with keys; fetched unless known, or always when refreshing.
    public func deviceKeys(of users: [Data], _ connection: Connection, refresh: Bool = false)
        async throws -> [Data: [UInt64: Data]]
    {
        let wanted = Array(Set(users))
        let missing = refresh ? wanted : wanted.filter { directory[$0] == nil }
        for start in stride(from: 0, to: missing.count, by: Self.maxUsersPerLookup) {
            let batch = Array(missing[start..<min(start + Self.maxUsersPerLookup, missing.count)])
            let found = try await connection.getDeviceKeys(userIDs: batch)
            // Unknown and deleted users are left out: they have no devices.
            for user in batch { directory[user] = [:] }
            for u in found {
                var devices: [UInt64: Data] = [:]
                for d in u.devices where d.publicKey.count == E2EE.keyLength {
                    devices[d.deviceID] = d.publicKey
                }
                directory[u.userID] = devices
                seen[u.userID, default: [:]].merge(devices) { $1 }
            }
        }
        return wanted.reduce(into: [:]) { $0[$1] = directory[$1] ?? [:] }
    }

    /// A user's fingerprint over their current devices.
    public func fingerprint(of user: Data, _ connection: Connection, refresh: Bool = false)
        async throws -> Data
    {
        let devices = try await deviceKeys(of: [user], connection, refresh: refresh)[user] ?? [:]
        return E2EE.fingerprint(userID: user, devices: devices)
    }

    /// The fingerprint from what is known, without asking; nil if the user is not known.
    public func knownFingerprint(of user: Data) -> Data? {
        directory[user].map { E2EE.fingerprint(userID: user, devices: $0) }
    }

    /// Follows a device event: a device of the user set its key or signed out. Returns whether
    /// the user's devices changed.
    @discardableResult
    public func noteDevice(user: Data, _ device: Wrinkle_V1_Device, signedOut: Bool) -> Bool {
        let key = device.publicKey
        guard signedOut || key.count == E2EE.keyLength else { return false }
        if !signedOut { seen[user, default: [:]][device.deviceID] = key }
        guard var devices = directory[user] else { return false }
        let before = devices
        devices[device.deviceID] = signedOut ? nil : key
        directory[user] = devices
        guard devices != before else { return false }
        for (id, roster) in rosters where roster.members.contains(user) { rosters[id] = nil }
        return true
    }

    /// Forgets a conversation's roster, as after a membership change.
    public func invalidate(_ conversationID: Data) {
        rosters[conversationID] = nil
    }

    // MARK: Receiving

    /// Fetches and stores the keys of epochs after the given one, by default the highest held.
    /// Returns the response for the roster.
    @discardableResult
    public func fetchKeys(_ conversationID: Data, _ connection: Connection, after: UInt32? = nil)
        async throws -> Wrinkle_V1_GetConversationKeysResponse
    {
        let resp = try await connection.getConversationKeys(
            conversationID: conversationID, afterEpoch: after ?? epochs.highest(conversationID))
        for k in resp.keys {
            guard try await knows(k.senderUserID, k.senderDeviceID, k.senderPublicKey, connection)
            else { continue }
            let context = E2EE.WrapContext(
                conversationID: conversationID, epoch: k.epoch, senderUserID: k.senderUserID,
                senderDeviceID: k.senderDeviceID, recipientUserID: userID,
                recipientDeviceID: deviceID)
            if let key = try? E2EE.unwrap(
                k.wrapped, recipient: privateKey, senderPublic: k.senderPublicKey, context: context)
            {
                epochs.put(conversationID, epoch: k.epoch, key: key)
            }
        }
        return resp
    }

    /// Whether the key is the one the directory has for the device.
    private func knows(_ user: Data, _ device: UInt64, _ key: Data, _ connection: Connection)
        async throws -> Bool
    {
        if seen[user]?[device] == nil {
            _ = try await deviceKeys(of: [user], connection, refresh: true)
        }
        return seen[user]?[device] == key
    }

    /// Opens a message's sealed content with a held key.
    public nonisolated func open(_ m: Wrinkle_V1_Message) -> Wrinkle_V1_Content? {
        epochs.open(m)
    }

    // MARK: Sending

    /// Seals the content and sends it with the attachment blobs it refers to, first publishing
    /// a new epoch if the conversation needs one; a send refused for a stale epoch refreshes
    /// and is retried once.
    public func send(
        content: Wrinkle_V1_Content, to conversationID: Data, members: Members,
        clientMessageID: String, blobIDs: [Data] = [], _ connection: Connection
    ) async throws -> Wrinkle_V1_Message {
        var (epoch, key) = try await currentEpoch(conversationID, members, connection)
        var retried = false
        while true {
            let sealed = try E2EE.seal(
                content, key: key,
                context: E2EE.ContentContext(
                    conversationID: conversationID, epoch: epoch, senderUserID: userID,
                    clientMessageID: clientMessageID),
                nonce: E2EE.randomNonce())
            do {
                return try await connection.sendMessage(
                    conversationID: conversationID, clientMessageID: clientMessageID,
                    sealed: sealed, blobIDs: blobIDs)
            } catch let e as BackendError where e.code == .failedPrecondition && !retried {
                retried = true
                rosters[conversationID] = nil
                (epoch, key) = try await currentEpoch(
                    conversationID, members, connection, refresh: true)
            }
        }
    }

    /// The epoch to seal with and its key: the current one if its recipients are the members'
    /// devices, else a new one, published. Losing a race to publish refreshes and tries again.
    public func currentEpoch(
        _ conversationID: Data, _ members: Members, _ connection: Connection,
        refresh: Bool = false
    ) async throws -> (UInt32, Data) {
        var refresh = refresh
        for _ in 0..<3 {
            let roster = try await roster(conversationID, members, connection, refresh: refresh)
            let devices = try await deviceKeys(of: roster.members, connection, refresh: refresh)
            var wanted: [Recipient: Data] = [:]
            for (user, keys) in devices {
                for (id, key) in keys { wanted[Recipient(userID: user, deviceID: id)] = key }
            }
            if !roster.rotate, roster.epoch > 0, roster.recipients == Set(wanted.keys),
                let key = epochs.key(conversationID, epoch: roster.epoch)
            {
                return (roster.epoch, key)
            }
            let next = roster.epoch + 1
            let key = E2EE.newEpochKey()
            do {
                try await publish(key, epoch: next, in: conversationID, for: wanted, connection)
            } catch let e as BackendError where e.code == .failedPrecondition {
                rosters[conversationID] = nil
                refresh = true
                continue
            }
            epochs.put(conversationID, epoch: next, key: key)
            rosters[conversationID] = Roster(
                epoch: next, rotate: false, recipients: Set(wanted.keys), members: roster.members)
            return (next, key)
        }
        throw BackendError(code: .failedPrecondition, message: "could not publish a key epoch")
    }

    private func roster(
        _ conversationID: Data, _ members: Members, _ connection: Connection, refresh: Bool
    ) async throws -> Roster {
        if !refresh, let r = rosters[conversationID] { return r }
        let resp = try await fetchKeys(conversationID, connection)
        let users: [Data]
        switch members {
        case .direct(let peer):
            users = [userID, peer]
        case .notes:
            users = [userID]
        case .group:
            users = try await connection.getMembers(conversationID: conversationID)
                .map(\.user.userID)
        }
        let r = Roster(
            epoch: resp.currentEpoch, rotate: resp.rotate,
            recipients: Set(
                resp.currentRecipients.map { Recipient(userID: $0.userID, deviceID: $0.deviceID) }),
            members: users)
        rosters[conversationID] = r
        return r
    }

    /// Wraps the key for every recipient device, by its public key, and publishes it, in batches: the first starts the
    /// epoch, the rest add to it.
    private func publish(
        _ key: Data, epoch: UInt32, in conversationID: Data, for recipients: [Recipient: Data],
        _ connection: Connection
    ) async throws {
        let ordered = recipients.keys.sorted {
            ($0.userID.hex, $0.deviceID) < ($1.userID.hex, $1.deviceID)
        }
        let wraps = try ordered.map { r in
            try wrap(
                key, epoch: epoch, in: conversationID, for: r, publicKey: recipients[r] ?? Data())
        }
        for start in stride(from: 0, to: wraps.count, by: Self.maxKeysPerPublish) {
            let end = min(start + Self.maxKeysPerPublish, wraps.count)
            try await connection.publishKeys(
                conversationID: conversationID, epoch: epoch, keys: Array(wraps[start..<end]))
        }
    }

    private func wrap(
        _ key: Data, epoch: UInt32, in conversationID: Data, for r: Recipient, publicKey: Data
    ) throws -> Wrinkle_V1_WrappedKey {
        let context = E2EE.WrapContext(
            conversationID: conversationID, epoch: epoch, senderUserID: userID,
            senderDeviceID: deviceID, recipientUserID: r.userID, recipientDeviceID: r.deviceID)
        let wrapped = try E2EE.wrap(
            key, sender: privateKey, recipientPublic: publicKey, context: context,
            nonce: E2EE.randomNonce())
        return .with {
            $0.recipientUserID = r.userID
            $0.recipientDeviceID = r.deviceID
            $0.wrapped = wrapped
        }
    }

    // MARK: History

    /// Wraps every epoch key held for another device of the user, after the user approved it.
    /// Conversations the user may no longer publish to are skipped. Returns the number of keys
    /// shared.
    @discardableResult
    public func shareHistory(withDevice device: UInt64, publicKey: Data, _ connection: Connection)
        async throws -> Int
    {
        guard publicKey.count == E2EE.keyLength, device != deviceID else { return 0 }
        seen[userID, default: [:]][device] = publicKey
        let r = Recipient(userID: userID, deviceID: device)
        var shared = 0
        for conversationID in epochs.conversations() {
            for (epoch, key) in epochs.all(conversationID).sorted(by: { $0.key < $1.key }) {
                let w = try wrap(
                    key, epoch: epoch, in: conversationID, for: r, publicKey: publicKey)
                do {
                    try await connection.publishKeys(
                        conversationID: conversationID, epoch: epoch, keys: [w])
                    shared += 1
                } catch let e as BackendError where e.code == .permissionDenied {
                    // A group the user is no longer in.
                    break
                }
            }
        }
        return shared
    }
}

extension KeyManager {
    /// Who is in a conversation: the user and a peer, a group's members, or the user alone in
    /// their notes.
    public enum Members: Sendable {
        case direct(peer: Data)
        case group
        case notes
    }

    /// Seals the text and sends it; see send(content:).
    public func send(
        _ text: String, to conversationID: Data, members: Members, clientMessageID: String,
        _ connection: Connection
    ) async throws -> Wrinkle_V1_Message {
        try await send(
            content: .with { $0.text = text }, to: conversationID, members: members,
            clientMessageID: clientMessageID, connection)
    }
}
