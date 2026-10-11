import Foundation

// MARK: Group requests

extension Connection {
    /// Creates a group; resending with the same clientGroupID returns the group made first.
    public func createGroup(topic: String, memberNames: [String], clientGroupID: String)
        async throws -> Wrinkle_V1_Conversation
    {
        try await call(
            .createGroup(
                .with {
                    $0.topic = topic
                    $0.memberNames = memberNames
                    $0.clientGroupID = clientGroupID
                })
        ).createGroup.conversation
    }

    public func addMembers(conversationID: Data, memberNames: [String]) async throws {
        _ = try await call(
            .addMembers(
                .with {
                    $0.conversationID = conversationID
                    $0.memberNames = memberNames
                }))
    }

    public func removeMember(conversationID: Data, userID: Data) async throws {
        _ = try await call(
            .removeMember(
                .with {
                    $0.conversationID = conversationID
                    $0.userID = userID
                }))
    }

    public func leaveGroup(conversationID: Data) async throws {
        _ = try await call(.leaveGroup(.with { $0.conversationID = conversationID }))
    }

    public func setTopic(conversationID: Data, topic: String) async throws {
        _ = try await call(
            .setTopic(
                .with {
                    $0.conversationID = conversationID
                    $0.topic = topic
                }))
    }

    public func setAdmin(conversationID: Data, userID: Data, admin: Bool) async throws {
        _ = try await call(
            .setAdmin(
                .with {
                    $0.conversationID = conversationID
                    $0.userID = userID
                    $0.admin = admin
                }))
    }

    /// A group's current members, those who joined first first.
    public func getMembers(conversationID: Data) async throws -> [Wrinkle_V1_Member] {
        try await call(.getMembers(.with { $0.conversationID = conversationID }))
            .getMembers.members
    }
}
