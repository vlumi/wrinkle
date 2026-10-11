import WrinkleCore

// MARK: Devices

extension AppState {
    public func loadDevices() async throws {
        let (connection, _) = try connected()
        devices = try await connection.listDevices()
    }

    public func renameDevice(_ id: UInt64, name: String) async throws {
        let (connection, _) = try connected()
        let device = try await connection.renameDevice(id: id, name: name)
        applyDevice(.with { $0.device = device })
    }

    /// Signs out another device; its connections close and it returns to sign-in.
    public func signOutDevice(_ id: UInt64) async throws {
        let (connection, _) = try connected()
        try await connection.signOutDevice(id: id)
        devices.removeAll { $0.deviceID == id }
    }

    /// Keeps the device list current from a device event.
    func applyDevice(_ e: Wrinkle_V1_DeviceEvent) {
        let id = e.device.deviceID
        devices.removeAll { $0.deviceID == id }
        if !e.signedOut { devices.append(e.device) }
        devices.sort {
            ($0.createdAt.seconds, $0.createdAt.nanos, $0.deviceID)
                < ($1.createdAt.seconds, $1.createdAt.nanos, $1.deviceID)
        }
    }
}
