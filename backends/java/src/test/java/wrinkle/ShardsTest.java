package wrinkle;

import static org.junit.jupiter.api.Assertions.assertEquals;

import java.util.HexFormat;
import java.util.List;
import org.junit.jupiter.api.Test;

class ShardsTest {
  private static final HexFormat HEX = HexFormat.of();

  @Test
  void keyspaceIdMatchesVitess() {
    // Pairs read from vtgate's xxhash vindex function.
    assertEquals(
        "ac758b75e4c3665c",
        HEX.formatHex(Shards.keyspaceId(HEX.parseHex("004eb6acc9704010b1387b332394a2db"))));
    assertEquals(
        "cd0363ddcc380d5b",
        HEX.formatHex(Shards.keyspaceId(HEX.parseHex("005dff9967944895b74f28fa37d8661a"))));
  }

  @Test
  void xxHash64ReferenceVectors() {
    // From the xxHash reference implementation, seed 0.
    assertEquals(0xEF46DB3751D8E999L, XxHash64.hash(new byte[0]));
    assertEquals(0x44BC2CF5AD770999L, XxHash64.hash("abc".getBytes()));
  }

  @Test
  void shardOfAnId() {
    Shards s = Shards.fromNames(List.of("80-", "-80"));
    Uuid ac = Uuid.fromHex("004eb6acc9704010b1387b332394a2db");
    Uuid cd = Uuid.fromHex("005dff9967944895b74f28fa37d8661a");
    assertEquals("80-", s.of(ac));
    assertEquals("80-", s.of(cd));
    Shards four = Shards.fromNames(List.of("-40", "40-80", "80-c0", "c0-"));
    assertEquals("80-c0", four.of(ac));
    assertEquals("c0-", four.of(cd));
    Shards fine = Shards.fromNames(List.of("-ac75", "ac75-ac76", "ac76-"));
    assertEquals("ac75-ac76", fine.of(ac));
  }

  @Test
  void unshardedIsOneShard() {
    Uuid id = Uuid.random();
    assertEquals("", Shards.ONE.of(id));
    assertEquals("", Shards.fromNames(List.of()).of(id));
  }
}
