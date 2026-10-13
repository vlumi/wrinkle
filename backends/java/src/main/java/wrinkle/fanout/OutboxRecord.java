package wrinkle.fanout;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.util.List;
import wrinkle.Kinds;
import wrinkle.Uuid;
import wrinkle.v1.Event;

/**
 * A row of a conversation's outbox as Debezium emits it after unwrapping: binary columns as base64
 * strings, unsigned bigints as numbers or decimal strings.
 */
record OutboxRecord(
    Uuid conversationId,
    long outboxId,
    List<Uuid> recipients,
    String conversationKind,
    String topic,
    int memberCount,
    String kind,
    Long targetOutboxId,
    Event event) {
  private static final ObjectMapper JSON = new ObjectMapper();

  static OutboxRecord parse(byte[] value) throws IOException {
    JsonNode n = JSON.readTree(value);
    List<Uuid> recipients;
    try {
      recipients = Uuid.split(field(n, "recipients").binaryValue());
    } catch (IllegalArgumentException e) {
      throw new IOException("recipients", e);
    }
    JsonNode topic = n.get("topic");
    JsonNode target = n.get("target_outbox_id");
    return new OutboxRecord(
        id(n, "conversation_id"),
        unsigned(n, "outbox_id"),
        recipients,
        field(n, "conversation_kind").asText(),
        topic == null || topic.isNull() ? "" : topic.asText(),
        (int) unsigned(n, "member_count"),
        field(n, "kind").asText(),
        target == null || target.isNull() ? null : unsigned(n, "target_outbox_id"),
        Event.parseFrom(field(n, "payload").binaryValue()));
  }

  boolean group() {
    return Kinds.GROUP.equals(conversationKind);
  }

  /** In a direct conversation, the other recipient; null in a group. */
  Uuid peerOf(Uuid userId) {
    if (group()) {
      return null;
    }
    for (Uuid r : recipients) {
      if (!r.equals(userId)) {
        return r;
      }
    }
    return null;
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
