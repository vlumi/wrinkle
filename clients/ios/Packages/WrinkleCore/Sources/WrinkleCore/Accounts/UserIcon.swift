/// The standard icons of the contract (User.icon), drawn as emoji.
public enum UserIcon {
    public static let all: [(key: String, emoji: String)] = [
        ("cat", "🐱"), ("dog", "🐶"), ("fox", "🦊"), ("owl", "🦉"),
        ("bear", "🐻"), ("frog", "🐸"), ("panda", "🐼"), ("rabbit", "🐰"),
        ("tiger", "🐯"), ("penguin", "🐧"), ("octopus", "🐙"), ("unicorn", "🦄"),
    ]

    /// The emoji for an icon key; nil for none or a key this client does not know.
    public static func emoji(for key: String) -> String? {
        all.first { $0.key == key }?.emoji
    }
}
