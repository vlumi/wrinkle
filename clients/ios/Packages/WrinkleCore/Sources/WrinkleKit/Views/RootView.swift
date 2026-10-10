import SwiftData
import SwiftUI
import WrinkleCore

/// The app: sign-in until a session exists, then the conversations.
public struct RootView: View {
    @Environment(AppState.self) private var app
    @Environment(\.modelContext) private var context

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
    }
}
