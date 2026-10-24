package wrinkle.push;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.google.protobuf.ByteString;
import java.util.Arrays;
import java.util.Base64;
import java.util.List;
import org.junit.jupiter.api.Test;
import wrinkle.Uuid;
import wrinkle.v1.Message;
import wrinkle.v1.Sealed;

class NotifierTest {
  private static final ObjectMapper JSON = new ObjectMapper();
  private static final Uuid CONVERSATION = Uuid.fromHex("42000000000000000000000000000000");

  private static byte[] repeat(int b, int n) {
    byte[] out = new byte[n];
    Arrays.fill(out, (byte) b);
    return out;
  }

  private static Message sealedMessage(int ciphertext) {
    return Message.newBuilder()
        .setSeq(7)
        .setSenderUserId(ByteString.copyFrom(repeat(0xab, 16)))
        .setClientMessageId("m-1")
        .setSealed(
            Sealed.newBuilder()
                .setEpoch(3)
                .setNonce(ByteString.copyFrom(repeat(1, 12)))
                .setCiphertext(ByteString.copyFrom(repeat(2, ciphertext))))
        .build();
  }

  @Test
  void carriesTheSealedContent() throws Exception {
    JsonNode body =
        JSON.readTree(Notifier.messageAlert("bob", 2, CONVERSATION, sealedMessage(100)).payload());
    JsonNode aps = body.get("aps");
    assertEquals(1, aps.get("mutable-content").asInt());
    assertEquals("New message", aps.get("alert").get("body").asText());
    assertEquals("bob", aps.get("alert").get("title").asText());
    assertEquals(2, aps.get("badge").asInt());
    assertEquals(CONVERSATION.hex(), aps.get("thread-id").asText());
    assertEquals(CONVERSATION.hex(), body.get("conversation_id").asText());
    assertEquals(7, body.get("seq").asLong());
    assertEquals(3, body.get("epoch").asLong());
    assertEquals(Base64.getEncoder().encodeToString(repeat(1, 12)), body.get("nonce").asText());
    assertEquals(
        Base64.getEncoder().encodeToString(repeat(2, 100)), body.get("ciphertext").asText());
    assertEquals("ab".repeat(16), body.get("sender_user_id").asText());
    assertEquals("m-1", body.get("client_message_id").asText());
  }

  @Test
  void leavesOutContentThatDoesNotFit() throws Exception {
    byte[] raw = Notifier.messageAlert("bob", 2, CONVERSATION, sealedMessage(4000)).payload();
    assertTrue(raw.length <= Apns.MAX_PAYLOAD, raw.length + " bytes");
    JsonNode body = JSON.readTree(raw);
    for (String k :
        List.of("epoch", "nonce", "ciphertext", "sender_user_id", "client_message_id")) {
      assertFalse(body.has(k), k + " present");
    }
    assertEquals(7, body.get("seq").asLong());
    assertEquals(1, body.get("aps").get("mutable-content").asInt());
  }
}
