package wrinkle;

import com.google.protobuf.Timestamp;
import java.time.Instant;
import java.time.LocalDateTime;
import java.time.ZoneOffset;

/** Times as the database stores them, UTC without a zone, and their wire form. */
public final class Timestamps {
  private Timestamps() {}

  public static LocalDateTime now() {
    return LocalDateTime.now(ZoneOffset.UTC);
  }

  public static Timestamp toProto(LocalDateTime t) {
    Instant i = t.toInstant(ZoneOffset.UTC);
    return Timestamp.newBuilder().setSeconds(i.getEpochSecond()).setNanos(i.getNano()).build();
  }

  public static LocalDateTime fromProto(Timestamp t) {
    return LocalDateTime.ofEpochSecond(t.getSeconds(), t.getNanos(), ZoneOffset.UTC);
  }
}
