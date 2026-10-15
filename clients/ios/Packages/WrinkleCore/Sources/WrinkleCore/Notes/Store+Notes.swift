import Foundation
import SwiftData

extension Store {
    /// The user's notes, if the device knows them.
    public func notesConversation() -> StoredConversation? {
        let notes = Wrinkle_V1_ConversationKind.notes.rawValue
        var d = FetchDescriptor<StoredConversation>(predicate: #Predicate { $0.kind == notes })
        d.fetchLimit = 1
        return try? context.fetch(d).first
    }
}

extension StoredConversation {
    public static let notesTitle = "Notes"

    /// The user's notes, with no one else in them.
    public var isNotes: Bool { kind == Wrinkle_V1_ConversationKind.notes.rawValue }

    /// A conversation with a peer.
    public var isDirect: Bool { kind == Wrinkle_V1_ConversationKind.direct.rawValue }
}
