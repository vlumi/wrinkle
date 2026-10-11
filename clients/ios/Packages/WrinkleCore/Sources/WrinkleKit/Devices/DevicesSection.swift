import SwiftUI
import WrinkleCore

/// The account's signed-in devices: renaming them and signing out the others.
struct DevicesSection: View {
    @Environment(AppState.self) private var app
    @State private var renaming: Wrinkle_V1_Device?
    @State private var newName = ""
    @State private var error: String?

    var body: some View {
        Section {
            ForEach(app.devices, id: \.deviceID) { device in
                row(device)
            }
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red)
            }
        } header: {
            Text("Devices")
        } footer: {
            Text("Signing out a device ends its session there.")
        }
        .task { await attempt { try await app.loadDevices() } }
        .alert(
            "Rename device",
            isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })
        ) {
            TextField("Name", text: $newName)
            Button("Rename") {
                if let device = renaming { rename(device) }
            }
            Button("Cancel", role: .cancel) {}
        }
    }

    private func row(_ device: Wrinkle_V1_Device) -> some View {
        let mine = device.deviceID == app.myDeviceID
        return HStack {
            VStack(alignment: .leading) {
                Text(device.name.isEmpty ? "Unnamed device" : device.name)
                Text(detail(device, mine: mine)).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            Menu {
                Button("Rename") {
                    newName = device.name
                    renaming = device
                }
                if !mine {
                    Button("Sign out", role: .destructive) { signOut(device) }
                }
            } label: {
                Image(systemName: "ellipsis.circle")
            }
            .accessibilityLabel("Device actions")
        }
    }

    private func detail(_ device: Wrinkle_V1_Device, mine: Bool) -> String {
        if mine { return "This device" }
        let seen = device.lastSeenAt.date.formatted(date: .abbreviated, time: .shortened)
        return "Last seen \(seen)"
    }

    private func rename(_ device: Wrinkle_V1_Device) {
        let name = newName.trimmingCharacters(in: .whitespaces)
        guard name.utf8.count <= 64 else {
            error = "Device names are at most 64 bytes."
            return
        }
        Task { await attempt { try await app.renameDevice(device.deviceID, name: name) } }
    }

    private func signOut(_ device: Wrinkle_V1_Device) {
        Task { await attempt { try await app.signOutDevice(device.deviceID) } }
    }

    private func attempt(_ work: () async throws -> Void) async {
        do {
            try await work()
            error = nil
        } catch {
            self.error = errorText(error, [.notFound: "That device is already signed out."])
        }
    }
}
