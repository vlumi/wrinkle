import PhotosUI
import SwiftUI
import UniformTypeIdentifiers
import WrinkleCore

/// The files picked to send, above the composer: a preview of each with its remove button, and
/// its upload progress while sending.
struct DraftStrip: View {
    @Environment(AppState.self) private var app
    let drafts: [OutgoingFile]
    let previews: [UUID: CGImage]
    let sending: Bool
    let remove: (UUID) -> Void

    static let side: CGFloat = 64

    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: 8) {
                ForEach(drafts) { d in chip(d) }
            }
            .padding(.horizontal)
            .padding(.top, 8)
        }
    }

    private func chip(_ d: OutgoingFile) -> some View {
        ZStack(alignment: .topTrailing) {
            Group {
                if let image = previews[d.id] {
                    Image(decorative: image, scale: 1).resizable().scaledToFill()
                } else {
                    VStack(spacing: 2) {
                        Image(systemName: d.isImage ? "photo" : "doc.fill").font(.title3)
                        Text(d.name).font(.caption2).lineLimit(1)
                    }
                    .padding(4)
                }
            }
            .frame(width: Self.side, height: Self.side)
            .background(Color.gray.opacity(0.2))
            .clipShape(.rect(cornerRadius: 8))
            .overlay(alignment: .bottom) {
                if sending, let p = app.uploadProgress[d.id] {
                    ProgressView(value: p).padding(4)
                }
            }
            if !sending {
                Button {
                    remove(d.id)
                } label: {
                    Image(systemName: "xmark.circle.fill").symbolRenderingMode(.palette)
                        .foregroundStyle(.white, .black.opacity(0.6))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Remove \(d.name)")
                .padding(2)
            }
        }
    }
}

/// Turns what the pickers return into files to send.
enum PickedFiles {
    static let previewSide = 160

    /// A picture from the photo library; its name made up from its type.
    static func load(_ item: PhotosPickerItem, index: Int) async throws -> OutgoingFile {
        guard let data = try await item.loadTransferable(type: Data.self) else {
            throw AccountError("That picture cannot be used.")
        }
        let type = item.supportedContentTypes.first
        let ext = type?.preferredFilenameExtension ?? "jpg"
        return OutgoingFile(
            data: data, name: "photo-\(index).\(ext)",
            mediaType: type?.preferredMIMEType ?? AttachmentImage.mediaType)
    }

    /// A file from the document picker, read within its security scope.
    static func load(_ url: URL) throws -> OutgoingFile {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let name = url.lastPathComponent
        let size = (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
        guard size <= AttachmentRules.maxFileBytes else {
            throw AccountError("\(name) is too large to send.")
        }
        let data = try Data(contentsOf: url)
        guard data.count <= AttachmentRules.maxFileBytes else {
            throw AccountError("\(name) is too large to send.")
        }
        return OutgoingFile(
            data: data, name: name, mediaType: OutgoingFile.mediaType(forName: name))
    }

    /// A small preview of a picture, or nil for other files.
    static func preview(_ file: OutgoingFile) async -> CGImage? {
        guard file.isImage else { return nil }
        let data = file.data
        return await Task.detached { AttachmentImage.decode(data, maxSide: previewSide) }.value
    }
}
