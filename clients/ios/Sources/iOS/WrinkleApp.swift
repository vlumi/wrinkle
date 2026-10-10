import SwiftData
import SwiftUI
import WrinkleCore
import WrinkleKit

@main
struct WrinkleApp: App {
    @State private var app = AppState()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(app)
        }
        .modelContainer(for: [StoredConversation.self, StoredMessage.self])
    }
}
