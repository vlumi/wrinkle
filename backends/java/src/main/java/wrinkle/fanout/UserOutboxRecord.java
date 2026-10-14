package wrinkle.fanout;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import wrinkle.Uuid;
import wrinkle.v1.Event;

/** A row of a user's outbox, a profile, device, palette or contact change, as Debezium emits it. */
record UserOutboxRecord(Uuid userId, long outboxId, String kind, Event event) {
  private static final ObjectMapper JSON = new ObjectMapper();

  static UserOutboxRecord parse(byte[] value) throws IOException {
    JsonNode n = JSON.readTree(value);
    return new UserOutboxRecord(
        OutboxRecord.id(n, "user_id"),
        OutboxRecord.unsigned(n, "outbox_id"),
        OutboxRecord.field(n, "kind").asText(),
        Event.parseFrom(OutboxRecord.field(n, "payload").binaryValue()));
  }
}
