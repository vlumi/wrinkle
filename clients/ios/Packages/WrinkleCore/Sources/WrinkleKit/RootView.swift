import SwiftData
import SwiftUI
import WrinkleCore

/// The app: sign-in until a session exists, then the conversations.
public struct RootView: View {
    @Environment(AppState.self) private var app
    @Environment(\.modelContext) private var context
    @Environment(\.scenePhase) private var scenePhase

    public init() {}

    public var body: some View {
        Group {
            if app.isSignedIn {
                ConversationListView()
            } else {
                SignInView()
            }
        }
        .task { app.resume(context: context) }
        .onChange(of: scenePhase) { _, phase in
            // Back on screen as soon as it leaves the background, even under a
            // system alert, which keeps the scene inactive rather than active.
            switch phase {
            case .background: app.enterBackground()
            default: app.enterForeground()
            }
        }
    }
}
