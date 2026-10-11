package wrinkle.fanout;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import wrinkle.Uuid;
import wrinkle.v1.Event;

/**
 * A row of a conversation's outbox as Debezium emits it after unwrapping: binary columns as base64
 * strings, unsigned bigints as numbers or decimal strings.
 */
record OutboxRecord(
    Uuid conversationId, long outboxId, Uuid userA, Uuid userB, String kind, Event event) {
  private static final ObjectMapper JSON = new ObjectMapper();

  static OutboxRecord parse(byte[] value) throws IOException {
    JsonNode n = JSON.readTree(value);
    return new OutboxRecord(
        id(n, "conversation_id"),
        unsigned(n, "outbox_id"),
        id(n, "user_a"),
        id(n, "user_b"),
        field(n, "kind").asText(),
        Event.parseFrom(field(n, "payload").binaryValue()));
  }

  Uuid peerOf(Uuid userId) {
    return userId.equals(userA) ? userB : userA;
  }

  static JsonNode field(JsonNode n, String name) throws IOException {
    JsonNode f = n.get(name);
    if (f == null || f.isNull()) {
      throw new IOException("no " + name);
    }
    return f;
  }

  /** A BINARY(16) column. */
  static Uuid id(JsonNode n, String name) throws IOException {
    byte[] b = field(n, name).binaryValue();
    Uuid id = Uuid.parse(b);
    if (id == null) {
      throw new IOException(name + " of " + (b == null ? 0 : b.length) + " bytes");
    }
    return id;
  }

  /** A BIGINT UNSIGNED column. */
  static long unsigned(JsonNode n, String name) throws IOException {
    try {
      return Long.parseUnsignedLong(field(n, name).asText());
    } catch (NumberFormatException e) {
      throw new IOException("unsigned bigint " + name, e);
    }
  }
}
