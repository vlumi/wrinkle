import Foundation
import WrinkleCore

// MARK: Groups

extension AppState {
    /// Creates a group and returns its id. A retry passes the same clientGroupID, so a group
    /// made by an attempt whose response was lost is returned rather than made again.
    public func createGroup(topic: String, memberNames: [String], clientGroupID: String)
        async throws -> IDHex
    {
        let (connection, store) = try connected()
        let c = try await connection.createGroup(
            topic: topic, memberNames: memberNames, clientGroupID: clientGroupID)
        let stored = store.upsert(c)
        store.save()
        return stored.id
    }

    /// Fetches the group's current members into the store. Someone no longer a member is
    /// refused; their cache stays as it was.
    public func loadMembers(conversationID: IDHex) async throws {
        let (connection, store) = try connected()
        let members = try await connection.getMembers(conversationID: try wireID(conversationID))
        store.replaceMembers(members, in: conversationID, me: myUserID)
        store.save()
        publishNames()
    }

    public func addMembers(_ names: [String], to conversationID: IDHex) async throws {
        let (connection, _) = try connected()
        try await connection.addMembers(
            conversationID: try wireID(conversationID), memberNames: names)
    }

    public func removeMember(_ userID: IDHex, from conversationID: IDHex) async throws {
        let (connection, _) = try connected()
        try await connection.removeMember(
            conversationID: try wireID(conversationID), userID: try wireID(userID))
    }

    public func setAdmin(_ userID: IDHex, admin: Bool, in conversationID: IDHex) async throws {
        let (connection, _) = try connected()
        try await connection.setAdmin(
            conversationID: try wireID(conversationID), userID: try wireID(userID), admin: admin)
    }

    public func setTopic(_ topic: String, of conversationID: IDHex) async throws {
        let (connection, _) = try connected()
        try await connection.setTopic(conversationID: try wireID(conversationID), topic: topic)
    }

    public func leaveGroup(_ conversationID: IDHex) async throws {
        let (connection, _) = try connected()
        try await connection.leaveGroup(conversationID: try wireID(conversationID))
    }

    /// A user's name in a conversation, as this device knows it.
    func name(of userID: IDHex, in conversationID: IDHex) -> String? {
        store?.name(of: userID, in: conversationID)
    }

    /// The sentence for a notice in a conversation.
    func sentence(_ notice: Wrinkle_V1_Notice, sender: IDHex, in conversationID: IDHex)
        -> String
    {
        NoticeText.sentence(
            notice, senderID: sender, sender: name(of: sender, in: conversationID), me: myUserID)
    }

    /// Refetches the members of groups the user is in whose membership the events changed;
    /// the notices themselves already updated the cache, this corrects what they could not.
    func refreshMembers(after events: [Wrinkle_V1_Event], _ store: Store) {
        var ids = Set<IDHex>()
        for e in events {
            guard case .message(let m) = e.body, case .notice(let n) = m.message.content,
                NoticeText.changesMembers(n)
            else { continue }
            ids.insert(m.message.conversationID.hex)
        }
        for id in ids {
            if let wire = Data.id(hex: id) { Task { await keys?.invalidate(wire) } }
        }
        for id in ids where store.conversation(id)?.member == true {
            Task { try? await loadMembers(conversationID: id) }
        }
    }
}
