package wrinkle;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.google.protobuf.Timestamp;
import org.junit.jupiter.api.Test;
import wrinkle.v1.Message;

class MessageBodyTest {
  @Test
  void keepsOnlyTheContent() {
    Message m = Message.newBuilder().setSeq(3).setClientMessageId("c").setText("hi").build();
    Message stored = MessageBody.decode(Message.newBuilder(), MessageBody.encode(m)).build();
    assertEquals(Message.newBuilder().setText("hi").build(), stored);
  }

  @Test
  void keepsOnlyDeletedAtOnceDeleted() {
    Timestamp at = Timestamp.newBuilder().setSeconds(100).build();
    Message m = Message.newBuilder().setSeq(3).setText("hi").setDeletedAt(at).build();
    Message stored =
        MessageBody.decode(Message.newBuilder().setSeq(3), MessageBody.encode(m)).build();
    assertEquals(Message.ContentCase.CONTENT_NOT_SET, stored.getContentCase());
    assertTrue(stored.hasDeletedAt());
    assertEquals(at, stored.getDeletedAt());
  }
}
