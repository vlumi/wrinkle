package wrinkle;

import java.sql.Connection;
import java.sql.ResultSet;
import java.sql.SQLException;
import java.sql.Statement;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HexFormat;
import java.util.List;
import java.util.TreeMap;
import javax.sql.DataSource;

/**
 * Maps ids sharded by the xxhash vindex to the shards of one keyspace, so work can be grouped into
 * one transaction per shard. With no shards listed, every id is in one shard, named "".
 */
public final class Shards {
  public static final Shards ONE = new Shards(List.of(), List.of());

  private final List<String> names;
  // Each shard's first keyspace id, ascending; empty for the first.
  private final List<byte[]> starts;

  private Shards(List<String> names, List<byte[]> starts) {
    this.names = names;
    this.starts = starts;
  }

  /** The keyspace's shards from names such as "-80" and "80-", in any order. */
  public static Shards fromNames(List<String> shardNames) {
    TreeMap<byte[], String> byStart = new TreeMap<>(Arrays::compareUnsigned);
    for (String name : shardNames) {
      int dash = name.indexOf('-');
      String start = dash < 0 ? name : name.substring(0, dash);
      byStart.put(HexFormat.of().parseHex(start), name);
    }
    return new Shards(new ArrayList<>(byStart.values()), new ArrayList<>(byStart.keySet()));
  }

  /** The keyspace's shards as vtgate lists them; a server that is not vtgate gives one shard. */
  public static Shards load(DataSource db, String keyspace) {
    List<String> names = new ArrayList<>();
    try (Connection c = db.getConnection();
        Statement s = c.createStatement();
        ResultSet rs = s.executeQuery("SHOW VITESS_SHARDS")) {
      while (rs.next()) {
        String row = rs.getString(1);
        int slash = row.indexOf('/');
        if (slash >= 0 && row.substring(0, slash).equals(keyspace)) {
          names.add(row.substring(slash + 1));
        }
      }
    } catch (SQLException e) {
      return ONE;
    }
    return fromNames(names);
  }

  /** The shards' names in keyspace order; none when the keyspace is not sharded. */
  public List<String> names() {
    return names;
  }

  /** Where the xxhash vindex places an id: its XXH64, little-endian. */
  public static byte[] keyspaceId(byte[] id) {
    long h = XxHash64.hash(id);
    byte[] b = new byte[8];
    for (int i = 0; i < 8; i++) {
      b[i] = (byte) (h >>> (8 * i));
    }
    return b;
  }

  /** The name of the shard holding the id. */
  public String of(Uuid id) {
    byte[] ksid = keyspaceId(id.bytes());
    String shard = "";
    for (int i = 0; i < starts.size(); i++) {
      if (Arrays.compareUnsigned(ksid, starts.get(i)) >= 0) {
        shard = names.get(i);
      }
    }
    return shard;
  }
}
