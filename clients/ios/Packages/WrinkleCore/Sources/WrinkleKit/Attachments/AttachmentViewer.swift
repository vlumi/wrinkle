import SwiftUI
import WrinkleCore

/// A picture full screen: its thumbnail at once, then the full image once downloaded, checked
/// and decrypted, with sharing.
struct AttachmentViewer: View {
    @Environment(AppState.self) private var app
    @Environment(\.dismiss) private var dismiss
    let attachment: Wrinkle_V1_Attachment
    @State private var image: CGImage?
    @State private var file: URL?
    @State private var error: String?

    nonisolated static let maxSide = 4096

    var body: some View {
        NavigationStack {
            ZStack {
                Color.black.ignoresSafeArea()
                if let image {
                    Image(decorative: image, scale: 1).resizable().scaledToFit()
                } else if let error {
                    Text(error).foregroundStyle(.white)
                } else {
                    ThumbnailView(attachment: attachment).scaledToFit().opacity(0.6)
                    ProgressView().tint(.white)
                }
            }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
                if let file {
                    ToolbarItem(placement: Toolbar.trailing) {
                        ShareLink(item: file)
                    }
                }
            }
        }
        .task {
            do {
                let url = try await app.attachmentFile(attachment)
                let data = try Data(contentsOf: url)
                image = await Task.detached { AttachmentImage.decode(data, maxSide: Self.maxSide) }
                    .value
                if image == nil { error = "This picture cannot be shown." }
                file = url
            } catch {
                self.error = errorText(error)
            }
        }
    }
}
