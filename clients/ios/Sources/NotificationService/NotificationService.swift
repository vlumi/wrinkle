import UserNotifications
import WrinkleCore

/// Words a message push: the backend sends "New message" and the sealed content, which the
/// epoch keys the app shares through the app group open. Without a key that does, or without
/// the sealed content (a payload that would have been too large), the generic body stays.
final class NotificationService: UNNotificationServiceExtension {
    override func didReceive(
        _ request: UNNotificationRequest,
        withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
    ) {
        guard let content = request.content.mutableCopy() as? UNMutableNotificationContent else {
            contentHandler(request.content)
            return
        }
        let vault = Vaults.shared()
        if let body = PushContent.body(
            content.userInfo, keys: EpochKeys(vault: vault), names: SharedNames.load(from: vault))
        {
            content.body = body
        }
        contentHandler(content)
    }

    /// The work above is synchronous and quick; nothing is left to cut short.
    override func serviceExtensionTimeWillExpire() {}
}
