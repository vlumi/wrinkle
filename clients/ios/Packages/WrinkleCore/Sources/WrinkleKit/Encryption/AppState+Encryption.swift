import Foundation
import WrinkleCore

/// The backend has another key for this device: its private key is lost, so it signs in again
/// as a new device.
struct KeyLost: Error {}

// MARK: Encryption

extension AppState {
    /// Loads or makes this device's key pair and publishes its public key.
    func startEncryption(_ connection: Connection, _ creds: Credentials) async throws {
        let keys: KeyManager
        if let current = self.keys, current.deviceID == creds.deviceID {
            keys = current
        } else {
            let (key, _) = DeviceKey.loadOrCreate(deviceID: creds.deviceID, in: deviceVault)
            keys = KeyManager(
                userID: creds.userID, deviceID: creds.deviceID, privateKey: key,
                epochs: EpochKeys(vault: sharedVault))
            self.keys = keys
        }
        triedEpochs = [:]
        do {
            try await keys.publish(connection)
        } catch let e as BackendError where e.code == .failedPrecondition {
            throw KeyLost()
        }
    }

    /// Forgets the keys and everything learned with them, as on signing out.
    func forgetEncryption(deviceID: UInt64?) {
        if let deviceID {
            DeviceKey.forget(deviceID: deviceID, in: deviceVault)
            SavedSession.forgetDecidedDevices(for: deviceID)
        }
        EpochKeys(vault: sharedVault).forgetAll()
        SharedNames.forget(in: sharedVault)
        keys = nil
        triedEpochs = [:]
        newDevice = nil
        securityNotices = []
        fingerprints = [:]
    }

    /// Opens what is locked, first fetching the keys of epochs not yet asked for; `force` asks
    /// again, as when the user opens the conversation.
    func openLocked(_ connection: Connection, only: IDHex? = nil, force: Bool = false) async {
        guard let store, let keys else { return }
        for (id, needed) in store.locked() where only == nil || only == id {
            guard let wire = Data.id(hex: id) else { continue }
            let missing = needed.filter { keys.epochs.key(wire, epoch: $0) == nil }
            let untried = force ? missing : missing.subtracting(triedEpochs[id] ?? [])
            if let low = untried.min(),
                (try? await keys.fetchKeys(wire, connection, after: low > 0 ? low - 1 : 0)) != nil
            {
                triedEpochs[id, default: []].formUnion(untried)
            }
            store.reopen(id)
        }
        store.save()
    }

    /// Asks again for the keys of the conversation's locked messages.
    public func unlock(conversationID: IDHex) async {
        guard let (connection, _) = try? connected() else { return }
        await openLocked(connection, only: conversationID, force: true)
    }

    /// Follows a device event: the user's own device list, the key directory, and a prompt
    /// for a new device of the user.
    func followDevice(_ e: Wrinkle_V1_DeviceEvent) {
        let user = e.userID.isEmpty ? (Data(hex: myUserID) ?? Data()) : e.userID
        let own = user.hex == myUserID
        if own { applyDevice(e) }
        if let keys {
            Task {
                let changed = await keys.noteDevice(user: user, e.device, signedOut: e.signedOut)
                if changed, let fp = await keys.knownFingerprint(of: user) {
                    fingerprints[user.hex] = fp
                }
            }
        }
        guard own, let mine = myDeviceID, e.device.deviceID != mine else { return }
        if e.signedOut {
            if newDevice?.deviceID == e.device.deviceID { newDevice = nil }
            return
        }
        guard e.device.publicKey.count == E2EE.keyLength,
            !SavedSession.decidedDevices(for: mine).contains(e.device.deviceID)
        else { return }
        newDevice = e.device
    }

    /// The user's answer to sharing the history with the new device: on yes, every epoch key
    /// this device holds is wrapped for it. A failure asks again.
    public func decideSharing(_ share: Bool) {
        guard let device = newDevice else { return }
        newDevice = nil
        guard share else {
            settle(device, shared: false)
            return
        }
        Task {
            do {
                let (connection, _) = try connected()
                guard let keys else { throw AccountError("Not connected.") }
                try await keys.shareHistory(
                    withDevice: device.deviceID, publicKey: device.publicKey, connection)
                settle(device, shared: true)
            } catch {
                securityNotices.append(
                    "Could not share your message history with \(Self.deviceLabel(device)).")
                newDevice = device
            }
        }
    }

    private func settle(_ device: Wrinkle_V1_Device, shared: Bool) {
        if let mine = myDeviceID { SavedSession.decide(device.deviceID, for: mine) }
        newDevice = nil
        let name = Self.deviceLabel(device)
        securityNotices.append(
            shared
                ? "\(name) signed in to your account and received your message history."
                : "\(name) signed in to your account without your message history.")
    }

    public func dismissSecurityNotice(_ notice: String) {
        securityNotices.removeAll { $0 == notice }
    }

    static func deviceLabel(_ device: Wrinkle_V1_Device) -> String {
        device.name.isEmpty ? "A new device" : device.name
    }

    // MARK: Verification

    /// The safety number with a contact, from both users' current devices, and the contact's
    /// fingerprint to mark as verified.
    public func safetyNumber(with userID: IDHex) async throws -> (number: String, fingerprint: Data)
    {
        let (connection, _) = try connected()
        guard let keys else { throw AccountError("Not connected.") }
        let theirs = try await keys.fingerprint(
            of: try wireID(userID), connection, refresh: true)
        let mine = try await keys.fingerprint(of: keys.userID, connection, refresh: true)
        fingerprints[userID] = theirs
        fingerprints[myUserID] = mine
        return (E2EE.safetyNumber(mine, theirs), theirs)
    }

    public func markVerified(_ userID: IDHex, fingerprint: Data) {
        store?.verify(userID, fingerprint: fingerprint)
        store?.save()
    }

    public func clearVerification(_ userID: IDHex) {
        store?.unverify(userID)
        store?.save()
    }

    /// Learns the users' current fingerprints, for the warnings on verified contacts.
    public func loadFingerprints(_ users: [IDHex]) async {
        guard let (connection, _) = try? connected(), let keys else { return }
        for user in users {
            guard let wire = Data.id(hex: user),
                let fp = try? await keys.fingerprint(of: wire, connection)
            else { continue }
            fingerprints[user] = fp
        }
    }

    /// How a contact's safety number stands.
    public func verificationState(_ userID: IDHex, verified: Data?) -> VerificationState {
        VerificationState(verified: verified, current: fingerprints[userID])
    }

    /// Writes the groups, names and blocked users the notification service extension words
    /// pushes with.
    func publishNames() {
        guard var shared = store?.sharedNames() else { return }
        shared.blocked = contacts.blocked
        shared.save(to: sharedVault)
    }
}
