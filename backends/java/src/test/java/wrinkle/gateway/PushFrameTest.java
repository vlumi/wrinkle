package wrinkle.gateway;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertNull;

import com.google.protobuf.ByteString;
import java.util.Arrays;
import org.junit.jupiter.api.Test;
import wrinkle.backend.v1.Push;
import wrinkle.backend.v1.SignedOut;
import wrinkle.v1.Event;
import wrinkle.v1.Frame;
import wrinkle.v1.ReadEvent;

class PushFrameTest {
  private static Event event(int textBytes) {
    return Event.newBuilder()
        .setCursor(7)
        .setRead(ReadEvent.newBuilder().setConversationId(ByteString.copyFrom(new byte[textBytes])))
        .build();
  }

  @Test
  void turnsAnEventPushIntoItsFrame() {
    for (int n : new int[] {0, 10, 200, 70_000}) {
      Event e = event(n);
      byte[] push = Push.newBuilder().setEvent(e.toByteString()).build().toByteArray();
      byte[] want = Frame.newBuilder().setEvent(e).build().toByteArray();
      assertArrayEquals(want, Connections.eventFrameOf(push));
      assertArrayEquals(want, Connections.eventFrame(e.toByteArray()));
    }
  }

  @Test
  void leavesAnythingElseToTheDecoder() {
    byte[] signedOut =
        Push.newBuilder()
            .setSignedOut(SignedOut.newBuilder().addDeviceIds(3))
            .build()
            .toByteArray();
    assertNull(Connections.eventFrameOf(signedOut));
    assertNull(Connections.eventFrameOf(new byte[0]));
    // Truncated, and followed by another field.
    byte[] push = Push.newBuilder().setEvent(event(5).toByteString()).build().toByteArray();
    assertNull(Connections.eventFrameOf(Arrays.copyOf(push, push.length - 1)));
    assertNull(Connections.eventFrameOf(Arrays.copyOf(push, push.length + 2)));
    // An unterminated length.
    assertNull(Connections.eventFrameOf(new byte[] {0x0a, (byte) 0x80}));
  }
}
