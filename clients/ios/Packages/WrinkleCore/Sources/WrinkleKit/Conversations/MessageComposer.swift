import PhotosUI
import SwiftUI
import WrinkleCore

/// Writing a message: text, a sticker, or up to ten pictures and files with the text as their
/// caption.
struct MessageComposer: View {
    let conversationID: IDHex
    /// What a refused send means: no longer in the group, or not allowed to message the peer.
    let refusal: String
    @Binding var showStickers: Bool
    @Binding var error: String?
    @Environment(AppState.self) private var app
    @State private var text = ""
    @State private var drafts: [OutgoingFile] = []
    @State private var previews: [UUID: CGImage] = [:]
    @State private var sending = false
    @State private var showPhotos = false
    @State private var showFiles = false
    @State private var photoItems: [PhotosPickerItem] = []

    private var trimmedText: String { text.trimmingCharacters(in: .whitespaces) }
    private var room: Int { max(0, AttachmentRules.maxItems - drafts.count) }
    private var canSend: Bool { !sending && (!trimmedText.isEmpty || !drafts.isEmpty) }

    var body: some View {
        VStack(spacing: 0) {
            if !drafts.isEmpty {
                DraftStrip(drafts: drafts, previews: previews, sending: sending, remove: remove)
            }
            HStack {
                Menu {
                    Button("Photos", systemImage: "photo.on.rectangle") { showPhotos = true }
                    Button("Files", systemImage: "doc") { showFiles = true }
                } label: {
                    Image(systemName: "paperclip")
                }
                .disabled(room == 0 || sending)
                .accessibilityLabel("Attach")
                Button {
                    showStickers = true
                } label: {
                    Image(systemName: "face.smiling")
                }
                .disabled(!drafts.isEmpty)
                .accessibilityLabel("Stickers")
                TextField(drafts.isEmpty ? "Message" : "Caption", text: $text)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit(send)
                Button("Send", action: send).disabled(!canSend)
            }
            .padding()
        }
        .photosPicker(
            isPresented: $showPhotos, selection: $photoItems, maxSelectionCount: max(1, room),
            matching: .images, photoLibrary: .shared()
        )
        .fileImporter(
            isPresented: $showFiles, allowedContentTypes: [.item], allowsMultipleSelection: true
        ) { result in
            if case .success(let urls) = result { add(files: urls) }
        }
        .onChange(of: photoItems) { _, items in
            guard !items.isEmpty else { return }
            photoItems = []
            add(photos: items)
        }
    }

    private func add(photos items: [PhotosPickerItem]) {
        Task {
            for item in items.prefix(room) {
                do {
                    append(try await PickedFiles.load(item, index: drafts.count + 1))
                } catch {
                    self.error = errorText(error)
                }
            }
        }
    }

    private func add(files urls: [URL]) {
        for url in urls.prefix(room) {
            do {
                append(try PickedFiles.load(url))
            } catch {
                self.error = errorText(error)
            }
        }
        if urls.count > room {
            error = "Send at most \(AttachmentRules.maxItems) files at a time."
        }
    }

    private func append(_ file: OutgoingFile) {
        guard drafts.count < AttachmentRules.maxItems else { return }
        drafts.append(file)
        Task { previews[file.id] = await PickedFiles.preview(file) }
    }

    private func remove(_ id: UUID) {
        drafts.removeAll { $0.id == id }
        previews[id] = nil
        if drafts.isEmpty { app.discardOutgoing(conversationID) }
    }

    private func send() {
        guard canSend else { return }
        let t = trimmedText
        guard !drafts.isEmpty else {
            text = ""
            Task {
                do {
                    try await app.send(text: t, in: conversationID)
                    error = nil
                } catch {
                    self.error = errorText(error, [.permissionDenied: refusal])
                }
            }
            return
        }
        let files = drafts
        sending = true
        Task {
            do {
                try await app.send(files: files, caption: t, in: conversationID)
                drafts = []
                previews = [:]
                text = ""
                error = nil
            } catch {
                self.error = errorText(
                    error,
                    [
                        .permissionDenied: refusal,
                        .failedPrecondition: "An attachment was not uploaded; try again.",
                    ])
            }
            sending = false
        }
    }
}
