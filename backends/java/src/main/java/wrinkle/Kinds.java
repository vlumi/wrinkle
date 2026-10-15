package wrinkle;

import wrinkle.v1.ConversationKind;

/** The kind column of conversation and user_conversation. */
public final class Kinds {
  private Kinds() {}

  public static final String DIRECT = "direct";
  public static final String GROUP = "group";
  public static final String NOTES = "notes";

  /** The contract's kind of a stored one. */
  public static ConversationKind contract(String kind) {
    return switch (kind) {
      case DIRECT -> ConversationKind.CONVERSATION_KIND_DIRECT;
      case GROUP -> ConversationKind.CONVERSATION_KIND_GROUP;
      case NOTES -> ConversationKind.CONVERSATION_KIND_NOTES;
      default -> ConversationKind.CONVERSATION_KIND_UNSPECIFIED;
    };
  }
}
