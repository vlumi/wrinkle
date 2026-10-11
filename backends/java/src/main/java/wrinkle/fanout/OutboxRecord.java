package wrinkle.fanout;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import wrinkle.v1.Event;

/** An outbox row as Debezium emits it: unsigned bigints as decimal strings, bytes as base64. */
record OutboxRecord(
    long conversationId, long outboxId, long userA, long userB, String kind, Event event) {
  private static final ObjectMapper JSON = new ObjectMapper();

  static OutboxRecord parse(byte[] value) throws IOException {
    JsonNode n = JSON.readTree(value);
    return new OutboxRecord(
        Long.parseLong(n.get("conversation_id").asText()),
        Long.parseLong(n.get("outbox_id").asText()),
        Long.parseLong(n.get("user_a").asText()),
        Long.parseLong(n.get("user_b").asText()),
        n.get("kind").asText(),
        Event.parseFrom(n.get("payload").binaryValue()));
  }

  long peerOf(long userId) {
    return userId == userA ? userB : userA;
  }
}
