package wrinkle.stickers;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.util.List;
import org.junit.jupiter.api.Test;
import wrinkle.Uuid;

class StickerServiceTest {
  private final Uuid a = Uuid.random();
  private final Uuid b = Uuid.random();
  private final Uuid c = Uuid.random();
  private final List<Uuid> order = List.of(a, b, c);

  @Test
  void movesBeforeAnother() {
    assertEquals(List.of(c, a, b), StickerService.moved(order, c, a));
    assertEquals(List.of(b, a, c), StickerService.moved(order, a, c));
    assertEquals(List.of(b, a, c), StickerService.moved(order, b, a));
  }

  @Test
  void movesToTheEnd() {
    assertEquals(List.of(b, c, a), StickerService.moved(order, a, null));
    assertEquals(order, StickerService.moved(order, c, null));
  }

  @Test
  void beforeItselfChangesNothing() {
    assertEquals(order, StickerService.moved(order, b, b));
    assertEquals(order, StickerService.moved(order, a, b));
  }
}
