import QuickLook
import SwiftUI
import WrinkleCore

/// A message's attachments in its bubble: pictures as thumbnails, in a grid when there are
/// several, opening a full-screen viewer; other files as rows that download, decrypt and open
/// in Quick Look.
struct AttachmentsView: View {
    @Environment(AppState.self) private var app
    let attachments: Wrinkle_V1_Attachments
    @State private var viewing: Wrinkle_V1_Attachment?
    @State private var preview: URL?
    @State private var opening: Data?
    @State private var error: String?

    static let singleWidth: CGFloat = 240
    static let gridSide: CGFloat = 116

    private var images: [Wrinkle_V1_Attachment] { attachments.items.filter(\.isImage) }
    private var files: [Wrinkle_V1_Attachment] { attachments.items.filter { !$0.isImage } }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if images.count == 1, let a = images.first {
                Button {
                    viewing = a
                } label: {
                    ThumbnailView(attachment: a)
                        .frame(
                            width: Self.singleWidth,
                            height: Self.singleWidth * CGFloat(a.height) / CGFloat(a.width)
                        )
                        .clipShape(.rect(cornerRadius: 8))
                }
                .buttonStyle(.plain)
            } else if !images.isEmpty {
                LazyVGrid(
                    columns: [GridItem(.fixed(Self.gridSide)), GridItem(.fixed(Self.gridSide))],
                    spacing: 4
                ) {
                    ForEach(images) { a in
                        Button {
                            viewing = a
                        } label: {
                            ThumbnailView(attachment: a)
                                .frame(width: Self.gridSide, height: Self.gridSide)
                                .clipShape(.rect(cornerRadius: 6))
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            ForEach(files) { a in fileRow(a) }
            if let error {
                Text(error).font(.caption).foregroundStyle(.red)
            }
        }
        .quickLookPreview($preview)
        .fullScreen(item: $viewing) { a in AttachmentViewer(attachment: a) }
    }

    private func fileRow(_ a: Wrinkle_V1_Attachment) -> some View {
        Button {
            open(a)
        } label: {
            HStack(spacing: 8) {
                Image(systemName: "doc.fill").font(.title2).foregroundStyle(.tint)
                VStack(alignment: .leading) {
                    Text(a.name.isEmpty ? "File" : a.name).lineLimit(1)
                    Text(ByteCountFormatter.string(fromByteCount: Int64(a.size), countStyle: .file))
                        .font(.caption).foregroundStyle(.secondary)
                }
                if opening == a.blobID { ProgressView() }
            }
        }
        .buttonStyle(.plain)
        .disabled(opening != nil)
    }

    private func open(_ a: Wrinkle_V1_Attachment) {
        opening = a.blobID
        Task {
            do {
                preview = try await app.attachmentFile(a)
                error = nil
            } catch {
                self.error = errorText(error)
            }
            opening = nil
        }
    }
}

/// An attachment's thumbnail from the sealed content, or a placeholder without one.
struct ThumbnailView: View {
    let attachment: Wrinkle_V1_Attachment
    @State private var image: CGImage?

    var body: some View {
        ZStack {
            Color.gray.opacity(0.2)
            if let image {
                Image(decorative: image, scale: 1).resizable().scaledToFill()
            } else {
                Image(systemName: "photo").font(.title).foregroundStyle(.secondary)
            }
        }
        .accessibilityLabel("Photo")
        .task(id: attachment.blobID) {
            let data = attachment.thumbnail
            guard !data.isEmpty else { return }
            image = await Task.detached {
                AttachmentImage.decode(data, maxSide: AttachmentImage.thumbnailSide)
            }.value
        }
    }
}
