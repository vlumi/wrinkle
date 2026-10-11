import PhotosUI
import SwiftUI
import WrinkleCore

/// Choosing a picture for the profile, or removing it.
struct PictureSection: View {
    @Environment(AppState.self) private var app
    @State private var item: PhotosPickerItem?
    @State private var busy = false
    @State private var error: String?

    private var hasPicture: Bool { !(app.me?.avatar.isEmpty ?? true) }

    var body: some View {
        Section {
            // The label closure is not main-actor isolated: it gets plain values.
            let title = hasPicture ? "Change picture" : "Choose picture"
            let working = busy
            PhotosPicker(selection: $item, matching: .images, photoLibrary: .shared()) {
                HStack {
                    Text(title)
                    if working {
                        Spacer()
                        ProgressView()
                    }
                }
            }
            .disabled(busy)
            if hasPicture {
                Button("Remove picture", role: .destructive, action: remove).disabled(busy)
            }
            if let error {
                Text(error).font(.footnote).foregroundStyle(.red)
            }
        } header: {
            Text("Picture")
        } footer: {
            Text("A picture is shown instead of the icon.")
        }
        .onChange(of: item) { _, picked in
            guard let picked else { return }
            item = nil
            upload(picked)
        }
    }

    private func upload(_ picked: PhotosPickerItem) {
        run {
            guard let data = try await picked.loadTransferable(type: Data.self) else {
                throw AccountError("That picture cannot be used.")
            }
            try await app.setAvatar(picture: data)
        }
    }

    private func remove() {
        run { try await app.removeAvatar() }
    }

    private func run(_ work: @escaping @MainActor () async throws -> Void) {
        busy = true
        Task {
            do {
                try await work()
                error = nil
            } catch {
                self.error = errorText(error)
            }
            busy = false
        }
    }
}
