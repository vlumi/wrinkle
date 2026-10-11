package wrinkle.fanout;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.List;
import org.junit.jupiter.api.Test;
import wrinkle.Uuid;
import wrinkle.v1.Message;
import wrinkle.v1.User;

// Records as Debezium wrote them, binary columns in base64.
class OutboxRecordTest {
  private static byte[] json(String s) {
    return s.getBytes(StandardCharsets.UTF_8);
  }

  @Test
  void parsesAConversationRecord() throws IOException {
    OutboxRecord o =
        OutboxRecord.parse(
            json(
                """
                {"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"6453981837480687173",\
                "recipients":"QdU4zkeJR2KIGb3J8piNBcm50CCOSk6KjTopI8AYktA=",\
                "conversation_kind":"direct","topic":"","member_count":2,"kind":"message",\
                "payload":"EgwIjaCu1gYQmKXepgNSQQo/ChALhfcWRpRGB5sKZoQEtR9MEAEaEEHVOM5HiUdiiBm9yfKYjQUiAW0qBmZyb20gYjIMCI2grtYGEJil3qYD",\
                "created_at":1791725581886543}"""));
    assertEquals(
        "C4X3FkaURgebCmaEBLUfTA==", Base64.getEncoder().encodeToString(o.conversationId().bytes()));
    assertEquals(6453981837480687173L, o.outboxId());
    assertEquals("message", o.kind());
    assertEquals(2, o.recipients().size());
    assertEquals(2, o.memberCount());
    assertFalse(o.group());
    Uuid a = o.recipients().get(0);
    Uuid b = o.recipients().get(1);
    assertEquals(b, o.peerOf(a));
    assertEquals(a, o.peerOf(b));
    Message msg = o.event().getMessage().getMessage();
    assertEquals(1, msg.getSeq());
    assertEquals("from b", msg.getText());
    assertTrue(o.conversationId().matches(msg.getConversationId()));
  }

  @Test
  void parsesAGroupRecord() throws IOException {
    Uuid[] ids = {Uuid.random(), Uuid.random(), Uuid.random()};
    String recipients = Base64.getEncoder().encodeToString(Uuid.join(List.of(ids)));
    OutboxRecord o =
        OutboxRecord.parse(
            json(
                """
                {"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"7",\
                "recipients":"%s","conversation_kind":"group","topic":"Hikes",\
                "member_count":3,"kind":"read","payload":"","created_at":1}"""
                    .formatted(recipients)));
    assertTrue(o.group());
    assertEquals(List.of(ids), o.recipients());
    assertEquals("Hikes", o.topic());
    assertEquals(3, o.memberCount());
    assertNull(o.peerOf(ids[0]));
  }

  @Test
  void rejectsRecipientsNotInWholeIds() {
    assertThrows(
        IOException.class,
        () ->
            OutboxRecord.parse(
                json(
                    """
                    {"conversation_id":"C4X3FkaURgebCmaEBLUfTA==","outbox_id":"7",\
                    "recipients":"AAEC","conversation_kind":"group","member_count":1,\
                    "kind":"read","payload":""}""")));
  }

  @Test
  void parsesAUserRecord() throws IOException {
    UserOutboxRecord o =
        UserOutboxRecord.parse(
            json(
                """
                {"user_id":"NhA4dCVFTBuu2mxC1WP1gw==","outbox_id":"1","kind":"profile",\
                "payload":"EgwIqqCu1gYQ0N6YiAFqLAoqChA2EDh0JUVMG67abELVY/WDEhFpY29uLTU3OWFiMWFjZjk3YRoDZm94",\
                "created_at":1791725610285618}"""));
    assertEquals(1, o.outboxId());
    assertEquals("profile", o.kind());
    User user = o.event().getProfile().getUser();
    assertTrue(o.userId().matches(user.getUserId()));
    assertEquals("icon-579ab1acf97a", user.getName());
    assertEquals("fox", user.getIcon());
  }

  @Test
  void rejectsShortIdsAndMalformedNumbers() {
    assertThrows(
        IOException.class,
        () ->
            UserOutboxRecord.parse(
                json(
                    """
                    {"user_id":"AAEC","outbox_id":1,"kind":"profile","payload":""}""")));
    assertThrows(
        IOException.class,
        () ->
            UserOutboxRecord.parse(
                json(
                    """
                    {"user_id":"NhA4dCVFTBuu2mxC1WP1gw==","outbox_id":"x","kind":"profile",\
                    "payload":""}""")));
  }
}
