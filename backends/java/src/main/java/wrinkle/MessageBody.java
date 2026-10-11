package wrinkle;

import com.google.protobuf.InvalidProtocolBufferException;
import wrinkle.v1.Message;

/**
 * A message's content as message.body and user_conversation.last_body store it: a serialized
 * Message with only its content set, sealed or a notice, or only deleted_at once deleted.
 */
public final class MessageBody {
  private MessageBody() {}

  public static byte[] encode(Message m) {
    Message.Builder b = Message.newBuilder();
    if (m.hasDeletedAt()) {
      return b.setDeletedAt(m.getDeletedAt()).build().toByteArray();
    }
    switch (m.getContentCase()) {
      case SEALED -> b.setSealed(m.getSealed());
      case NOTICE -> b.setNotice(m.getNotice());
      default -> {}
    }
    return b.build().toByteArray();
  }

  /** Sets the stored content on the message. */
  public static Message.Builder decode(Message.Builder m, byte[] body) {
    try {
      return m.mergeFrom(body);
    } catch (InvalidProtocolBufferException e) {
      throw new IllegalStateException("corrupt message body", e);
    }
  }
}
