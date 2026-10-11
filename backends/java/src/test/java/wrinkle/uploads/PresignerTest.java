package wrinkle.uploads;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.time.Instant;
import java.util.Map;
import org.junit.jupiter.api.Test;

class PresignerTest {
  // The test vectors every backend shares.
  private static final Presigner PRESIGNER =
      new Presigner(
          "GK7772696e6b6c6500000000",
          "667c7e6aafc73808d30a89b14f18816b605d7d340823d9493b32b97aa7079fc4",
          "garage");
  private static final Instant T = Instant.parse("2026-01-02T03:04:05Z");
  private static final String PATH = "/blobs/00112233445566778899aabbccddeeff";

  @Test
  void presignsPut() {
    assertEquals(
        PATH
            + "?X-Amz-Algorithm=AWS4-HMAC-SHA256"
            + "&X-Amz-Credential=GK7772696e6b6c6500000000%2F20260102%2Fgarage%2Fs3%2Faws4_request"
            + "&X-Amz-Date=20260102T030405Z&X-Amz-Expires=900"
            + "&X-Amz-SignedHeaders=content-length%3Bcontent-type%3Bhost"
            + "&X-Amz-Signature=41f91cfa6894289b36a84ba3dd2382a597515b97882580eba97a7beead3006e3",
        PRESIGNER.presign(
            "PUT", PATH, Map.of("Content-Type", "image/png", "content-length", "1234"), T, 900));
  }

  @Test
  void presignsDelete() {
    assertEquals(
        PATH
            + "?X-Amz-Algorithm=AWS4-HMAC-SHA256"
            + "&X-Amz-Credential=GK7772696e6b6c6500000000%2F20260102%2Fgarage%2Fs3%2Faws4_request"
            + "&X-Amz-Date=20260102T030405Z&X-Amz-Expires=60&X-Amz-SignedHeaders=host"
            + "&X-Amz-Signature=63ca246240ae5e8ba3f157a95f71ddd3e094ee79692510754b5f3406b17b5a15",
        PRESIGNER.presign("DELETE", PATH, Map.of(), T, 60));
  }

  @Test
  void encodesEveryReservedByte() {
    assertEquals("a-Z_0.~%2F%3B%20%C3%A9", Presigner.encode("a-Z_0.~/; é"));
  }
}
