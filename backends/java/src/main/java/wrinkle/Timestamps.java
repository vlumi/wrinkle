package wrinkle;

import com.google.protobuf.Timestamp;
import java.time.Instant;
import java.time.LocalDateTime;
import java.time.ZoneOffset;
import java.time.temporal.ChronoUnit;

/** Times as the database stores them, UTC without a zone, and their wire form. */
public final class Timestamps {
  private Timestamps() {}

  /** Now, as DATETIME(6) stores it, so a response matches the row read later. */
  public static LocalDateTime now() {
    return LocalDateTime.now(ZoneOffset.UTC).truncatedTo(ChronoUnit.MICROS);
  }

  public static Timestamp toProto(LocalDateTime t) {
    Instant i = t.toInstant(ZoneOffset.UTC);
    return Timestamp.newBuilder().setSeconds(i.getEpochSecond()).setNanos(i.getNano()).build();
  }

  public static LocalDateTime fromProto(Timestamp t) {
    return LocalDateTime.ofEpochSecond(t.getSeconds(), t.getNanos(), ZoneOffset.UTC);
  }
}
