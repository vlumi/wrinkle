import SwiftData
import SwiftUI
import WrinkleCore

/// A contact's safety number, to compare with theirs, and whether the user verified it.
struct SafetyNumberView: View {
    let user: Wrinkle_V1_User
    @Environment(AppState.self) private var app
    @Query private var verifications: [StoredVerification]
    @State private var number: String?
    @State private var fingerprint: Data?
    @State private var error: String?

    init(user: Wrinkle_V1_User) {
        self.user = user
        let id = user.userID.hex
        _verifications = Query(filter: #Predicate<StoredVerification> { $0.userID == id })
    }

    private var userID: IDHex { user.userID.hex }
    private var name: String {
        user.name.isEmpty && !user.deleted
            ? "Someone" : UserDisplay.name(user.name, deleted: user.deleted)
    }
    private var state: VerificationState {
        let verified = verifications.first?.fingerprint
        guard let fingerprint else { return app.verificationState(userID, verified: verified) }
        return VerificationState(verified: verified, current: fingerprint)
    }

    var body: some View {
        Form {
            Section {
                HStack(spacing: 12) {
                    Avatar(user: user, size: 48)
                    Text(name).font(.title3)
                }
            }
            Section {
                if let number {
                    Text(Self.lines(number))
                        .font(.system(.title2, design: .monospaced))
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: .infinity)
                        .textSelection(.enabled)
                } else if error == nil {
                    ProgressView().frame(maxWidth: .infinity)
                }
            } header: {
                Text("Safety number")
            } footer: {
                Text(
                    "Compare this number with the one \(name) sees, in person or on a call. "
                        + "If they match, your messages reach only your and their devices.")
            }
            Section {
                switch state {
                case .verified:
                    Label("Verified", systemImage: "checkmark.seal.fill").foregroundStyle(.green)
                    Button("Clear verification") { app.clearVerification(userID) }
                case .changed:
                    Label(
                        "The safety number changed since you verified it.",
                        systemImage: "exclamationmark.triangle.fill"
                    )
                    .foregroundStyle(.orange)
                    verifyButton
                case .unverified:
                    verifyButton
                }
            }
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red)
            }
        }
        .navigationTitle(name)
        .inlineTitle()
        .task(id: app.me?.userID) { await load() }
        // A device event changed the contact's devices.
        .onChange(of: app.fingerprints[userID]) { _, new in
            if new != fingerprint { Task { await load() } }
        }
    }

    private var verifyButton: some View {
        Button("Mark as verified") {
            if let fingerprint { app.markVerified(userID, fingerprint: fingerprint) }
        }
        .disabled(fingerprint == nil)
    }

    private func load() async {
        do {
            let result = try await app.safetyNumber(with: userID)
            number = result.number
            fingerprint = result.fingerprint
            error = nil
        } catch {
            self.error = errorText(error)
        }
    }

    /// Six groups as two lines of three.
    static func lines(_ number: String) -> String {
        let groups = number.split(separator: " ")
        guard groups.count == 6 else { return number }
        return groups[0..<3].joined(separator: " ") + "\n" + groups[3..<6].joined(separator: " ")
    }
}
