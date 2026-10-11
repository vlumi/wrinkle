/// How users are shown.
public enum UserDisplay {
    public static let deletedName = "Deleted account"

    /// The name to show for a user: a deleted account's name is empty and free for others.
    public static func name(_ name: String, deleted: Bool) -> String {
        deleted ? deletedName : name
    }
}
