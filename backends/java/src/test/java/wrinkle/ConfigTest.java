package wrinkle;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;

class ConfigTest {
  @Test
  void parsesDotEnv() {
    assertEquals(
        Map.of("A", "1", "B", "two words", "C", "~/keys/AuthKey_X.p8", "D", ""),
        Config.parseDotEnv(
            List.of(
                "# a comment",
                "",
                "A=1",
                "export B=\"two words\"",
                "  C = '~/keys/AuthKey_X.p8'  ",
                "D=",
                "not a setting")));
  }

  @Test
  void expandsHome() {
    String home = System.getProperty("user.home");
    assertEquals(home + "/k.p8", Config.expandHome("~/k.p8"));
    assertEquals("/k.p8", Config.expandHome("/k.p8"));
    assertEquals("", Config.expandHome(""));
  }
}
