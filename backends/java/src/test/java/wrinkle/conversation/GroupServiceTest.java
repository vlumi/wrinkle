package wrinkle.conversation;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;

class GroupServiceTest {
  @Test
  void validatesTopics() {
    assertTrue(GroupService.validTopic("Hikes"));
    assertTrue(GroupService.validTopic("x".repeat(200)));
    assertTrue(GroupService.validTopic("é".repeat(100)));
    assertTrue(GroupService.validTopic("weekend plans"));
    assertFalse(GroupService.validTopic(""));
    assertFalse(GroupService.validTopic("x".repeat(201)));
    assertFalse(GroupService.validTopic("é".repeat(100) + "x"));
    assertFalse(GroupService.validTopic(" Hikes"));
    assertFalse(GroupService.validTopic("Hikes "));
    assertFalse(GroupService.validTopic("Hi\u0007kes"));
  }

  @Test
  void noticeClientIdsAreNoticeAndHex() {
    String id = Rows.noticeClientId();
    assertTrue(id.matches("notice-[0-9a-f]{16}"), id);
  }
}
