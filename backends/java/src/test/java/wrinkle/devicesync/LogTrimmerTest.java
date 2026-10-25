package wrinkle.devicesync;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import wrinkle.Uuid;

class LogTrimmerTest {
  @Test
  void cutsEachLogBelowItsNewestEventInThePage() {
    Uuid a = Uuid.random();
    Uuid b = Uuid.random();
    // By time, so a's positions may come out of order.
    List<LogTrimmer.Event> page =
        List.of(
            new LogTrimmer.Event(a, 3),
            new LogTrimmer.Event(b, 1),
            new LogTrimmer.Event(a, 5),
            new LogTrimmer.Event(a, 4),
            new LogTrimmer.Event(b, 2));
    Map<Uuid, Long> tails = LogTrimmer.tails(page);
    assertEquals(Map.of(a, 5L, b, 2L), tails);
    assertEquals(List.of(a, b), List.copyOf(tails.keySet()));
  }
}
