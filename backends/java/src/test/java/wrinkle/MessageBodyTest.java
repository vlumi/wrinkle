package wrinkle;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.google.protobuf.ByteString;
import com.google.protobuf.Timestamp;
import org.junit.jupiter.api.Test;
import wrinkle.v1.Message;
import wrinkle.v1.Sealed;

class MessageBodyTest {
  private static final Sealed SEALED =
      Sealed.newBuilder()
          .setEpoch(1)
          .setNonce(ByteString.copyFrom(new byte[12]))
          .setCiphertext(ByteString.copyFromUtf8("sealed"))
          .build();

  @Test
  void keepsOnlyTheContent() {
    Message m = Message.newBuilder().setSeq(3).setClientMessageId("c").setSealed(SEALED).build();
    Message stored = MessageBody.decode(Message.newBuilder(), MessageBody.encode(m)).build();
    assertEquals(Message.newBuilder().setSealed(SEALED).build(), stored);
  }

  @Test
  void keepsTheAttachmentIds() {
    ByteString blob = ByteString.copyFrom(new byte[16]);
    Message m = Message.newBuilder().setSeq(3).setSealed(SEALED).addBlobIds(blob).build();
    Message stored = MessageBody.decode(Message.newBuilder(), MessageBody.encode(m)).build();
    assertEquals(Message.newBuilder().setSealed(SEALED).addBlobIds(blob).build(), stored);
  }

  @Test
  void keepsOnlyDeletedAtOnceDeleted() {
    Timestamp at = Timestamp.newBuilder().setSeconds(100).build();
    Message m =
        Message.newBuilder()
            .setSeq(3)
            .setSealed(SEALED)
            .addBlobIds(ByteString.copyFrom(new byte[16]))
            .setDeletedAt(at)
            .build();
    Message stored =
        MessageBody.decode(Message.newBuilder().setSeq(3), MessageBody.encode(m)).build();
    assertEquals(Message.ContentCase.CONTENT_NOT_SET, stored.getContentCase());
    assertTrue(stored.hasDeletedAt());
    assertEquals(at, stored.getDeletedAt());
    assertEquals(0, stored.getBlobIdsCount());
  }
}
