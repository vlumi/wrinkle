import SwiftData
import SwiftUI
import UIKit
import UserNotifications
import WrinkleCore
import WrinkleKit

@main
struct WrinkleApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    private let container = Self.makeContainer()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(delegate.app)
        }
        .modelContainer(container)
    }

    /// The store is a cache of the backend: if it cannot be opened, the app runs on an
    /// in-memory one and fetches again.
    private static func makeContainer() -> ModelContainer {
        if let container = try? Store.container() { return container }
        let memory = ModelConfiguration(isStoredInMemoryOnly: true)
        // An in-memory container of the app's own models cannot fail.
        // swiftlint:disable:next force_try
        return try! ModelContainer(for: Store.schema, configurations: memory)
    }
}

/// Owns the session so push registration and notification taps, which
/// arrive through UIKit, reach it.
@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    let app = AppState()

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        Task {
            let granted =
                (try? await center.requestAuthorization(options: [.alert, .badge, .sound]))
            if granted == true { application.registerForRemoteNotifications() }
        }
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        let token = deviceToken.map { String(format: "%02x", $0) }.joined()
        // Debug builds are signed for development and get sandbox pushes.
        #if DEBUG
        app.setPushToken(token, sandbox: true)
        #else
        app.setPushToken(token, sandbox: false)
        #endif
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        // The simulator and unsigned builds land here; the app works without push.
    }

    /// A tapped notification opens its conversation.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse
    ) async {
        let info = response.notification.request.content.userInfo
        // The payload carries the conversation id as lowercase hex.
        guard let id = info["conversation_id"] as? String, Data.id(hex: id) != nil else { return }
        await MainActor.run { app.pendingConversation = id }
    }

    /// The backend does not push to a connected device; anything that does
    /// arrive in the foreground is already on screen.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        []
    }
}
