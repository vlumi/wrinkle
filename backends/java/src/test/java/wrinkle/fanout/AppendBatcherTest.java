package wrinkle.fanout;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.sql.SQLException;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import org.junit.jupiter.api.Test;
import wrinkle.Kinds;
import wrinkle.Shards;
import wrinkle.Uuid;
import wrinkle.fanout.AppendBatcher.Pending;
import wrinkle.v1.Event;
import wrinkle.v1.Message;
import wrinkle.v1.MessageEvent;
import wrinkle.v1.Notice;
import wrinkle.v1.ReadEvent;

// Which rows are batched, how a batch numbers its copies, and how batches gather per shard.
class AppendBatcherTest {
  private final Uuid alice = Uuid.random();
  private final Uuid bob = Uuid.random();

  private static OutboxRecord row(Event event, Uuid... recipients) {
    return new OutboxRecord(
        Uuid.random(), 1, List.of(recipients), Kinds.GROUP, "t", 2, "message", null, event);
  }

  private Event message(Message.Builder m) {
    return Event.newBuilder()
        .setMessage(MessageEvent.newBuilder().setMessage(m.setSenderUserId(alice.toByteString())))
        .build();
  }

  private Pending pending(Uuid userId) {
    return new Pending(
        userId, row(message(Message.newBuilder().setSeq(7)), alice, bob), null, null);
  }

  @Test
  void onlyMessagesWithoutNoticesAreBatched() {
    assertTrue(AppendBatcher.batchable(row(message(Message.newBuilder()))));
    assertFalse(
        AppendBatcher.batchable(
            row(message(Message.newBuilder().setNotice(Notice.getDefaultInstance())))));
    assertFalse(
        AppendBatcher.batchable(
            row(Event.newBuilder().setRead(ReadEvent.getDefaultInstance()).build())));
  }

  @Test
  void copiesNumberedAfterEachHeadInOrder() {
    Uuid gone = Uuid.random();
    List<Pending> batch =
        List.of(pending(alice), pending(bob), pending(gone), pending(alice), pending(alice));
    Map<Uuid, Long> heads = new HashMap<>(Map.of(alice, 10L, bob, 0L));
    List<Pending> stored = AppendBatcher.number(batch, heads);
    assertEquals(4, stored.size());
    assertEquals(11, batch.get(0).event.getCursor());
    assertEquals(1, batch.get(1).event.getCursor());
    assertNull(batch.get(2).event);
    assertEquals(12, batch.get(3).event.getCursor());
    assertEquals(13, batch.get(4).event.getCursor());
    assertEquals(13L, heads.get(alice));
    assertEquals(batch.get(4).event, parse(batch.get(4).payload));
  }

  private static Event parse(byte[] payload) {
    try {
      return Event.parseFrom(payload);
    } catch (Exception e) {
      throw new AssertionError(e);
    }
  }

  @Test
  void ownMessageMovesOwnReadPosition() {
    Pending own = pending(alice);
    Pending other = pending(bob);
    AppendBatcher.number(List.of(own, other), new HashMap<>(Map.of(alice, 0L, bob, 0L)));
    List<Object> ownRow = AppendBatcher.entryRow(own);
    List<Object> otherRow = AppendBatcher.entryRow(other);
    assertEquals(20, ownRow.size());
    assertEquals(7L, ownRow.get(17));
    assertEquals(0L, otherRow.get(17));
    assertEquals(true, ownRow.get(10));
    assertEquals(false, ownRow.get(11));
  }

  @Test
  void multiRowValues() {
    assertEquals("(?, ?), (?, ?), (?, ?)", AppendBatcher.rows(3, 2));
  }

  @Test
  void batchesGatherWhileOneIsStored() throws Exception {
    CountDownLatch storing = new CountDownLatch(1);
    CountDownLatch release = new CountDownLatch(1);
    List<Integer> sizes = new ArrayList<>();
    AppendBatcher b =
        new AppendBatcher(
            Shards.ONE,
            batch -> {
              synchronized (sizes) {
                sizes.add(batch.size());
              }
              storing.countDown();
              release.await();
            });
    List<Pending> all = new ArrayList<>();
    all.add(pending(Uuid.random()));
    b.add(all.getFirst());
    assertTrue(storing.await(5, TimeUnit.SECONDS));
    for (int i = 0; i < AppendBatcher.MAX_BATCH + 10; i++) {
      Pending p = pending(Uuid.random());
      all.add(p);
      b.add(p);
    }
    release.countDown();
    for (Pending p : all) {
      assertTrue(p.done.await(5, TimeUnit.SECONDS));
      assertFalse(p.failed);
    }
    // The first alone, then what queued meanwhile, at most a full batch at a time.
    assertEquals(List.of(1, AppendBatcher.MAX_BATCH, 10), sizes);
  }

  @Test
  void failedBatchMarksEveryCopy() throws Exception {
    AppendBatcher b =
        new AppendBatcher(
            Shards.ONE,
            batch -> {
              AppendBatcher.number(batch, new HashMap<>(Map.of(alice, 0L)));
              throw new SQLException("Duplicate entry", "23000", 1062);
            });
    Pending p = pending(alice);
    b.add(p);
    assertTrue(p.done.await(5, TimeUnit.SECONDS));
    assertTrue(p.failed);
    assertNull(p.event);
  }

  @Test
  void eachShardHasItsOwnWorker() throws Exception {
    Uuid low = Uuid.fromHex("004eb6acc9704010b1387b332394a2db"); // keyspace id ac...
    Uuid high = Uuid.fromHex("005dff9967944895b74f28fa37d8661a"); // keyspace id cd...
    Shards shards = Shards.fromNames(List.of("-c0", "c0-"));
    CountDownLatch release = new CountDownLatch(1);
    AppendBatcher b =
        new AppendBatcher(
            shards,
            batch -> {
              if (batch.getFirst().userId.equals(low)) {
                release.await();
              }
            });
    Pending blocked = pending(low);
    Pending other = pending(high);
    b.add(blocked);
    b.add(other);
    assertTrue(other.done.await(5, TimeUnit.SECONDS));
    assertEquals(1, blocked.done.getCount());
    release.countDown();
    assertTrue(blocked.done.await(5, TimeUnit.SECONDS));
  }
}
