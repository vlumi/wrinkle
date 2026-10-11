/// Limits the backend checks for groups, checked here too to say what is wrong.
public enum GroupRules {
    public static let maxTopicBytes = 200
    public static let topicTooLong = "Topics are at most 200 bytes."
}
