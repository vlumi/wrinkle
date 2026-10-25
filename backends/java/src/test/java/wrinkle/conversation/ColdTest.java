package wrinkle.conversation;

import static org.junit.jupiter.api.Assertions.assertArrayEquals;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.google.protobuf.ByteString;
import java.time.LocalDateTime;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HexFormat;
import java.util.List;
import org.apache.hadoop.hbase.Cell;
import org.apache.hadoop.hbase.client.Put;
import org.apache.hadoop.hbase.client.Result;
import org.junit.jupiter.api.Test;
import wrinkle.MessageBody;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Message;
import wrinkle.v1.Notice;
import wrinkle.v1.Reaction;
import wrinkle.v1.Sealed;

class ColdTest {
  private static Uuid id(String hex) {
    return Uuid.of(HexFormat.of().parseHex(hex));
  }

  @Test
  void keysSortBySeqWithinTheConversation() {
    Uuid conv = id("ab000000000000000000000000000001");
    assertEquals(
        "ab000000000000000000000000000001" + "0000000000000102",
        HexFormat.of().formatHex(Cold.key(conv, 0x0102)));
    assertTrue(Arrays.compareUnsigned(Cold.key(conv, 255), Cold.key(conv, 256)) < 0);
    // A conversation's last row sorts before the next conversation's first.
    Uuid next = id("ab000000000000000000000000000002");
    assertTrue(Arrays.compareUnsigned(Cold.key(conv, -1L), Cold.key(next, 0)) < 0);
  }

  private static Result read(Put put) {
    List<Cell> cells = new ArrayList<>(put.getFamilyCellMap().get(Cold.FAMILY));
    return Result.create(cells);
  }

  @Test
  void rowsRoundTrip() {
    Uuid conv = id("42000000000000000000000000000000");
    Sealed sealed =
        Sealed.newBuilder()
            .setEpoch(3)
            .setNonce(ByteString.copyFromUtf8("n"))
            .setCiphertext(ByteString.copyFromUtf8("c"))
            .build();
    byte[] body = MessageBody.encode(Message.newBuilder().setSealed(sealed).build());
    Reaction reaction =
        Reaction.newBuilder()
            .setUserId(id("07000000000000000000000000000000").toByteString())
            .setSealed(Sealed.newBuilder().setEpoch(3).setNonce(ByteString.copyFromUtf8("r")))
            .setUpdatedAt(Timestamps.toProto(LocalDateTime.of(2026, 10, 25, 1, 2, 3, 5000)))
            .build();
    byte[] blobs = new byte[32];
    Arrays.fill(blobs, (byte) 0xbb);
    Cold.Row row =
        new Cold.Row(
            9,
            id("01000000000000000000000000000000"),
            "c1",
            body,
            LocalDateTime.of(2023, 11, 14, 22, 13, 20, 123_456_000),
            77,
            blobs,
            Cold.encodeReactions(List.of(reaction)));
    Cold.Row got = Cold.Row.of(read(row.put(conv)));
    assertEquals(row.seq(), got.seq());
    assertEquals(row.sender(), got.sender());
    assertEquals(row.clientId(), got.clientId());
    assertArrayEquals(row.body(), got.body());
    assertEquals(row.sentAt(), got.sentAt());
    assertEquals(row.outboxId(), got.outboxId());
    assertArrayEquals(row.blobIds(), got.blobIds());

    Message msg = got.message(conv);
    assertEquals(9, msg.getSeq());
    assertEquals(3, msg.getSealed().getEpoch());
    assertEquals(2, msg.getBlobIdsCount());
    assertEquals(List.of(reaction), msg.getReactionsList());

    // A deleted message and a notice keep no reactions.
    byte[] deleted =
        MessageBody.encode(
            Message.newBuilder().setDeletedAt(Timestamps.toProto(Timestamps.now())).build());
    Message gone = got.withBody(deleted, got.blobIds(), got.reactions()).message(conv);
    assertTrue(gone.hasDeletedAt());
    assertEquals(0, gone.getReactionsCount());
    byte[] notice = MessageBody.encode(Message.newBuilder().setNotice(Notice.newBuilder()).build());
    assertEquals(
        0, got.withBody(notice, new byte[0], got.reactions()).message(conv).getReactionsCount());
    assertEquals(0, Cold.encodeReactions(List.of()).length);
  }

  private static List<Message> page(long... seqs) {
    List<Message> page = new ArrayList<>();
    for (long s : seqs) {
      page.add(Message.newBuilder().setSeq(s).build());
    }
    return page;
  }

  private record Case(
      String name,
      List<Message> page,
      long joined,
      long upTo,
      long before,
      long cold,
      int limit,
      long hi,
      boolean more,
      boolean again) {}

  @Test
  void pagesGoOnInTheColdTier() {
    long all = Long.MAX_VALUE;
    for (Case c :
        List.of(
            new Case("all hot, page full", page(12, 11), 1, all, all, 10, 2, 10, false, false),
            new Case("goes on below the lowest", page(12, 11), 1, all, all, 10, 5, 10, true, false),
            new Case("none in MySQL", page(), 1, all, all, 10, 5, 10, true, false),
            new Case("before the watermark", page(), 1, all, 4, 10, 5, 3, true, false),
            new Case("until a member left", page(), 1, 6, all, 10, 5, 6, true, false),
            new Case("nothing cold", page(3, 2, 1), 1, all, all, 0, 5, 0, false, false),
            new Case("joined after the watermark", page(12), 11, all, all, 10, 5, 10, false, false),
            new Case("before the first", page(), 1, all, 1, 10, 5, 0, false, false),
            new Case("moved meanwhile", page(12, 11, 10), 1, all, all, 10, 5, 0, false, true))) {
      ConversationService.ColdRange r =
          ConversationService.coldRange(
              c.page(), c.joined(), c.upTo(), c.before(), c.cold(), c.limit());
      assertEquals(c.more(), r.more(), c.name());
      assertEquals(c.again(), r.again(), c.name());
      if (c.more()) {
        assertEquals(c.hi(), r.hi(), c.name());
      } else {
        assertFalse(r.more(), c.name());
      }
    }
  }
}
