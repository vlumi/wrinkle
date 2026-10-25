package wrinkle.conversation;

import com.google.protobuf.InvalidProtocolBufferException;
import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.sql.Connection;
import java.sql.SQLException;
import java.time.LocalDateTime;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.List;
import javax.sql.DataSource;
import org.apache.hadoop.conf.Configuration;
import org.apache.hadoop.hbase.Cell;
import org.apache.hadoop.hbase.CellUtil;
import org.apache.hadoop.hbase.HBaseConfiguration;
import org.apache.hadoop.hbase.HConstants;
import org.apache.hadoop.hbase.TableName;
import org.apache.hadoop.hbase.client.ConnectionFactory;
import org.apache.hadoop.hbase.client.Get;
import org.apache.hadoop.hbase.client.Put;
import org.apache.hadoop.hbase.client.Result;
import org.apache.hadoop.hbase.client.ResultScanner;
import org.apache.hadoop.hbase.client.Scan;
import org.apache.hadoop.hbase.client.Table;
import org.apache.hadoop.hbase.security.User;
import org.apache.hadoop.security.UserGroupInformation;
import wrinkle.MessageBody;
import wrinkle.Sql;
import wrinkle.Timestamps;
import wrinkle.Uuid;
import wrinkle.v1.Message;
import wrinkle.v1.Reaction;

/**
 * The cold tier of the history, in HBase. Every write puts a whole row, so a write can be repeated.
 */
public final class Cold {
  private static final TableName TABLE = TableName.valueOf("message");
  static final byte[] FAMILY = bytes("m");
  private static final byte[] SENDER = bytes("sender");
  private static final byte[] CLIENT_ID = bytes("client_id");
  private static final byte[] BODY = bytes("body");
  private static final byte[] SENT_AT = bytes("sent_at");
  private static final byte[] OUTBOX_ID = bytes("outbox_id");
  private static final byte[] BLOB_IDS = bytes("blob_ids");
  private static final byte[] REACTIONS = bytes("reactions");
  // Bounds each get and put, retries included.
  private static final int OPERATION_TIMEOUT_MILLIS = 10_000;
  private static final int RPC_TIMEOUT_MILLIS = 5_000;

  /**
   * A message row as the cold tier keeps it: the columns of the message table and the message's
   * reactions, a serialized Message with only its reactions, oldest first, and empty for none.
   */
  record Row(
      long seq,
      Uuid sender,
      String clientId,
      byte[] body,
      LocalDateTime sentAt,
      long outboxId,
      byte[] blobIds,
      byte[] reactions) {

    Row withBody(byte[] body, byte[] blobIds, byte[] reactions) {
      return new Row(seq, sender, clientId, body, sentAt, outboxId, blobIds, reactions);
    }

    Row withReactions(byte[] reactions) {
      return withBody(body, blobIds, reactions);
    }

    Put put(Uuid conversationId) {
      long micros = sentAt.toEpochSecond(ZoneOffset.UTC) * 1_000_000 + sentAt.getNano() / 1000;
      return new Put(key(conversationId, seq))
          .addColumn(FAMILY, SENDER, sender.bytes())
          .addColumn(FAMILY, CLIENT_ID, bytes(clientId))
          .addColumn(FAMILY, BODY, body)
          .addColumn(FAMILY, SENT_AT, ByteBuffer.allocate(8).putLong(micros).array())
          .addColumn(FAMILY, OUTBOX_ID, ByteBuffer.allocate(8).putLong(outboxId).array())
          .addColumn(FAMILY, BLOB_IDS, blobIds)
          .addColumn(FAMILY, REACTIONS, reactions);
    }

    static Row of(Result r) {
      long seq = 0;
      byte[] key = r.getRow();
      if (key != null && key.length == Uuid.LENGTH + 8) {
        seq = ByteBuffer.wrap(key, Uuid.LENGTH, 8).getLong();
      }
      Uuid sender = null;
      String clientId = "";
      byte[] body = new byte[0];
      LocalDateTime sentAt = null;
      long outboxId = 0;
      byte[] blobIds = new byte[0];
      byte[] reactions = new byte[0];
      for (Cell c : r.rawCells()) {
        byte[] v = CellUtil.cloneValue(c);
        switch (new String(CellUtil.cloneQualifier(c), StandardCharsets.UTF_8)) {
          case "sender" -> sender = Uuid.of(v);
          case "client_id" -> clientId = new String(v, StandardCharsets.UTF_8);
          case "body" -> body = v;
          case "sent_at" -> {
            long micros = fixed64("sent_at", v);
            sentAt =
                LocalDateTime.ofEpochSecond(
                    Math.floorDiv(micros, 1_000_000),
                    Math.floorMod(micros, 1_000_000) * 1000,
                    ZoneOffset.UTC);
          }
          case "outbox_id" -> outboxId = fixed64("outbox_id", v);
          case "blob_ids" -> blobIds = v;
          case "reactions" -> reactions = v;
          default -> {}
        }
      }
      return new Row(seq, sender, clientId, body, sentAt, outboxId, blobIds, reactions);
    }

    /** The message as history returns it, with its reactions unless it is deleted or a notice. */
    Message message(Uuid conversationId) {
      Message.Builder m =
          Message.newBuilder()
              .setConversationId(conversationId.toByteString())
              .setSeq(seq)
              .setSenderUserId(sender.toByteString())
              .setClientMessageId(clientId)
              .setSentAt(Timestamps.toProto(sentAt));
      MessageBody.decode(m, body).clearBlobIds();
      for (Uuid id : Uuid.split(blobIds)) {
        m.addBlobIds(id.toByteString());
      }
      if (!m.hasDeletedAt() && !m.hasNotice()) {
        m.addAllReactions(reactionList());
      }
      return m.build();
    }

    List<Reaction> reactionList() {
      try {
        return Message.parseFrom(reactions).getReactionsList();
      } catch (InvalidProtocolBufferException e) {
        throw new IllegalStateException("corrupt cold reactions", e);
      }
    }
  }

  static byte[] encodeReactions(List<Reaction> reactions) {
    if (reactions.isEmpty()) {
      return new byte[0];
    }
    return Message.newBuilder().addAllReactions(reactions).build().toByteArray();
  }

  /**
   * The row key: the conversation id, then the seq big-endian, so a conversation's history is one
   * range in seq order.
   */
  static byte[] key(Uuid conversationId, long seq) {
    return ByteBuffer.allocate(Uuid.LENGTH + 8).put(conversationId.bytes()).putLong(seq).array();
  }

  private static long fixed64(String column, byte[] v) {
    if (v.length != 8) {
      throw new IllegalStateException("cold message " + column + " of " + v.length + " bytes");
    }
    return ByteBuffer.wrap(v).getLong();
  }

  private static byte[] bytes(String s) {
    return s.getBytes(StandardCharsets.UTF_8);
  }

  private final Configuration conf;
  private final Object connecting = new Object();
  private volatile org.apache.hadoop.hbase.client.Connection connection;

  /** Connects through the ZooKeeper quorum, on first use. */
  public Cold(String quorum) {
    conf = HBaseConfiguration.create();
    conf.set(HConstants.ZOOKEEPER_QUORUM, quorum);
    conf.setInt(HConstants.HBASE_CLIENT_OPERATION_TIMEOUT, OPERATION_TIMEOUT_MILLIS);
    conf.setInt(HConstants.HBASE_RPC_TIMEOUT_KEY, RPC_TIMEOUT_MILLIS);
  }

  private Table table() throws IOException {
    org.apache.hadoop.hbase.client.Connection c = connection;
    if (c == null) {
      synchronized (connecting) {
        c = connection;
        if (c == null) {
          // A user of its own: the current one is found through Subject.getSubject, which JDK 25
          // no longer supports.
          User user =
              User.create(UserGroupInformation.createRemoteUser(System.getProperty("user.name")));
          c = ConnectionFactory.createConnection(conf, user);
          connection = c;
        }
      }
    }
    return c.getTable(TABLE);
  }

  public void close() {
    synchronized (connecting) {
      if (connection != null) {
        try {
          connection.close();
        } catch (IOException e) {
          // Closing anyway.
        }
      }
    }
  }

  /** Writes the conversation's messages. */
  void put(Uuid conversationId, List<Row> rows) {
    List<Put> puts = new ArrayList<>(rows.size());
    for (Row r : rows) {
      puts.add(r.put(conversationId));
    }
    try (Table t = table()) {
      t.put(puts);
    } catch (IOException e) {
      throw new UncheckedIOException("cold put", e);
    }
  }

  /** The message at seq, or null if the cold tier does not have it. */
  Row get(Uuid conversationId, long seq) {
    try (Table t = table()) {
      Result r = t.get(new Get(key(conversationId, seq)).addFamily(FAMILY));
      return r.isEmpty() ? null : Row.of(r);
    } catch (IOException e) {
      throw new UncheckedIOException("cold get", e);
    }
  }

  /** Up to limit messages with seq from lo to hi, newest first. */
  List<Row> scan(Uuid conversationId, long lo, long hi, int limit) {
    List<Row> rows = new ArrayList<>();
    if (lo > hi || limit <= 0) {
      return rows;
    }
    Scan scan =
        new Scan()
            .withStartRow(key(conversationId, hi), true)
            .withStopRow(key(conversationId, lo), true)
            .setReversed(true)
            .setLimit(limit)
            .setCaching(limit)
            .addFamily(FAMILY);
    try (Table t = table();
        ResultScanner scanner = t.getScanner(scan)) {
      for (Result r : scanner) {
        rows.add(Row.of(r));
        if (rows.size() == limit) {
          break;
        }
      }
    } catch (IOException e) {
      throw new UncheckedIOException("cold scan", e);
    }
    return rows;
  }

  /** Changes to cold rows inside one transaction, and the rows as they were before. */
  final class Changes {
    private final List<Uuid> conversations = new ArrayList<>();
    private final List<Row> before = new ArrayList<>();

    /**
     * Writes a changed row inside the transaction that tells of the change, which holds the
     * conversation's row.
     */
    void change(Uuid conversationId, Row old, Row changed) {
      put(conversationId, List.of(changed));
      conversations.add(conversationId);
      before.add(old);
    }

    private void undo(Exception cause) {
      for (int i = before.size() - 1; i >= 0; i--) {
        try {
          put(conversations.get(i), List.of(before.get(i)));
        } catch (RuntimeException e) {
          cause.addSuppressed(e);
        }
      }
    }
  }

  /** Work in a transaction that may change cold rows. */
  @FunctionalInterface
  interface Work<T> {
    T run(Connection c, Changes changes) throws SQLException;
  }

  /**
   * Runs the work in one transaction, as {@link Sql#inTransaction}; the cold rows it changed are
   * put back unless the transaction commits.
   */
  <T> T inTransaction(DataSource db, Work<T> work) throws SQLException {
    Changes changes = new Changes();
    try {
      return Sql.inTransaction(db, c -> work.run(c, changes));
    } catch (SQLException | RuntimeException e) {
      changes.undo(e);
      throw e;
    }
  }
}
