package wrinkle;

import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.UnknownHostException;
import java.util.Arrays;
import java.util.HashSet;
import java.util.Set;

/** Flags with the same names and defaults as the Go backend, overridable by environment. */
public record Config(
    Set<String> roles,
    InetSocketAddress listen,
    String mysql,
    String kafka,
    String valkey,
    String services,
    InetSocketAddress servicesListen,
    String node,
    int dbConns,
    String blobUrl,
    String blobKeyId,
    String blobSecret,
    String blobRegion) {

  private static final Set<String> ROLES = Set.of("gateway", "fanout", "services");

  public boolean runsGateway() {
    return roles.contains("gateway");
  }

  public boolean runsServices() {
    return roles.contains("services");
  }

  public boolean runsFanout() {
    return roles.contains("fanout");
  }

  /** A gateway that forwards to a services process instead of running the services in-process. */
  public boolean forwardsToServices() {
    return runsGateway() && !services.isEmpty();
  }

  public static Config parse(String[] args) {
    String roles = env("WRINKLE_ROLES", "gateway,fanout");
    String listen = env("WRINKLE_LISTEN", ":8080");
    String mysql = env("WRINKLE_MYSQL", "127.0.0.1:15003");
    String kafka = env("WRINKLE_KAFKA", "localhost:19092");
    String valkey = env("WRINKLE_VALKEY", "localhost:6379");
    String services = env("WRINKLE_SERVICES", "");
    String servicesListen = env("WRINKLE_SERVICES_LISTEN", ":9000");
    String node = env("WRINKLE_NODE", hostname());
    String dbConns = env("WRINKLE_DB_CONNS", "64");
    // The local object storage's credentials; fine to commit.
    String blobUrl = env("WRINKLE_BLOB_URL", "http://127.0.0.1:9200");
    String blobKeyId = env("WRINKLE_BLOB_KEY_ID", "GK7772696e6b6c6500000000");
    String blobSecret =
        env(
            "WRINKLE_BLOB_SECRET",
            "667c7e6aafc73808d30a89b14f18816b605d7d340823d9493b32b97aa7079fc4");
    String blobRegion = env("WRINKLE_BLOB_REGION", "garage");
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
        case "services" -> services = value;
        case "services-listen" -> servicesListen = value;
        case "node" -> node = value;
        case "db-conns" -> dbConns = value;
        case "blob-url" -> blobUrl = value;
        case "blob-key-id" -> blobKeyId = value;
        case "blob-secret" -> blobSecret = value;
        case "blob-region" -> blobRegion = value;
        default -> throw new IllegalArgumentException("unknown flag " + a);
      }
    }
    Set<String> roleSet = new HashSet<>(Arrays.asList(roles.split(",")));
    for (String r : roleSet) {
      if (!ROLES.contains(r)) {
        throw new IllegalArgumentException("unknown role " + r);
      }
    }
    return new Config(
        roleSet,
        address(listen),
        mysql,
        kafka,
        valkey,
        services,
        address(servicesListen),
        node,
        Integer.parseInt(dbConns),
        blobUrl,
        blobKeyId,
        blobSecret,
        blobRegion);
  }

  /** host:port, or :port for every interface. */
  static InetSocketAddress address(String hostPort) {
    int colon = hostPort.lastIndexOf(':');
    int port = Integer.parseInt(hostPort.substring(colon + 1));
    String host = hostPort.substring(0, Math.max(colon, 0));
    return host.isEmpty() ? new InetSocketAddress(port) : new InetSocketAddress(host, port);
  }

  private static String env(String key, String fallback) {
    String v = System.getenv(key);
    return v == null || v.isEmpty() ? fallback : v;
  }

  private static String hostname() {
    try {
      return InetAddress.getLocalHost().getHostName();
    } catch (UnknownHostException e) {
      return "localhost";
    }
  }
}
