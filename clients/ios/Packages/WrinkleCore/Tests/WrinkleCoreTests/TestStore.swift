import Foundation
import SwiftData

@testable import WrinkleCore

extension Store {
    /// A store on an in-memory container, with the given epoch keys.
    static func inMemory(keys: EpochKeys? = nil) throws -> Store {
        let container = try ModelContainer(
            for: schema, configurations: ModelConfiguration(isStoredInMemoryOnly: true))
        return Store(context: ModelContext(container), keys: keys)
    }
}

extension Wrinkle_V1_Event {
    /// A text message as the log delivers it, sealed with the test key.
    static func message(
        _ seq: UInt64, in conversationID: Data, from sender: Data, _ text: String
    ) -> Wrinkle_V1_Event {
        .with {
            $0.cursor = seq
            $0.message.message = .with {
                $0.conversationID = conversationID
                $0.seq = seq
                $0.senderUserID = sender
                $0.clientMessageID = "m\(seq)"
                $0.sentAt = .init(date: Date())
                $0.seal(text)
            }
        }
    }
}
