package wrinkle;

import java.util.Arrays;
import java.util.HashSet;
import java.util.Set;

/** Flags with the same names and defaults as the Go backend, overridable by environment. */
public record Config(
    Set<String> roles,
    int port,
    String mysql,
    String kafka,
    String valkey,
    String node,
    int dbConns) {

  public static Config parse(String[] args) {
    String roles = env("WRINKLE_ROLES", "gateway,fanout");
    String listen = env("WRINKLE_LISTEN", ":8080");
    String mysql = env("WRINKLE_MYSQL", "127.0.0.1:15003");
    String kafka = env("WRINKLE_KAFKA", "localhost:19092");
    String valkey = env("WRINKLE_VALKEY", "localhost:6379");
    String node = env("WRINKLE_NODE", hostname());
    int dbConns = 64;
    for (int i = 0; i < args.length; i++) {
      String a = args[i];
      String value = null;
      int eq = a.indexOf('=');
      if (eq > 0) {
        value = a.substring(eq + 1);
        a = a.substring(0, eq);
      } else if (i + 1 < args.length) {
        value = args[++i];
      }
      if (value == null) {
        throw new IllegalArgumentException("flag " + a + " needs a value");
      }
      switch (a.replaceFirst("^--?", "")) {
        case "roles" -> roles = value;
        case "listen" -> listen = value;
        case "mysql" -> mysql = value;
        case "kafka" -> kafka = value;
        case "valkey" -> valkey = value;
        case "node" -> node = value;
        case "db-conns" -> dbConns = Integer.parseInt(value);
        default -> throw new IllegalArgumentException("unknown flag " + a);
      }
    }
    Set<String> roleSet = new HashSet<>(Arrays.asList(roles.split(",")));
    for (String r : roleSet) {
      if (!r.equals("gateway") && !r.equals("fanout")) {
        throw new IllegalArgumentException("unknown role " + r);
      }
    }
    int port = Integer.parseInt(listen.substring(listen.lastIndexOf(':') + 1));
    return new Config(roleSet, port, mysql, kafka, valkey, node, dbConns);
  }

  private static String env(String key, String fallback) {
    String v = System.getenv(key);
    return v == null || v.isEmpty() ? fallback : v;
  }

  private static String hostname() {
    try {
      return java.net.InetAddress.getLocalHost().getHostName();
    } catch (java.net.UnknownHostException e) {
      return "localhost";
    }
  }
}
