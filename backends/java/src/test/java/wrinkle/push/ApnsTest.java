package wrinkle.push;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.sun.net.httpserver.Headers;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.PublicKey;
import java.security.Signature;
import java.security.spec.ECGenParameterSpec;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Base64;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class ApnsTest {
  private static final ObjectMapper JSON = new ObjectMapper();
  private static final Duration TIMEOUT = Duration.ofSeconds(5);

  @TempDir Path dir;
  private HttpServer server;
  private final List<Seen> seen = new ArrayList<>();
  private int status = 200;
  private String reply = "";

  private record Seen(String method, String path, Headers headers, JsonNode body) {}

  @AfterEach
  void stop() {
    if (server != null) {
      server.stop(0);
    }
  }

  /** A local server standing in for APNs; returns its URL. */
  private String serve() throws IOException {
    server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
    server.createContext(
        "/",
        ex -> {
          JsonNode body = JSON.readTree(ex.getRequestBody().readAllBytes());
          synchronized (seen) {
            seen.add(
                new Seen(
                    ex.getRequestMethod(),
                    ex.getRequestURI().getPath(),
                    ex.getRequestHeaders(),
                    body));
          }
          byte[] out = reply.getBytes(StandardCharsets.UTF_8);
          ex.sendResponseHeaders(status, out.length == 0 ? -1 : out.length);
          if (out.length > 0) {
            ex.getResponseBody().write(out);
          }
          ex.close();
        });
    server.start();
    return "http://127.0.0.1:" + server.getAddress().getPort();
  }

  private record TestKey(PublicKey pub, Path path) {}

  private TestKey testKey() throws Exception {
    KeyPairGenerator g = KeyPairGenerator.getInstance("EC");
    g.initialize(new ECGenParameterSpec("secp256r1"));
    KeyPair kp = g.generateKeyPair();
    String pem =
        "-----BEGIN PRIVATE KEY-----\n"
            + Base64.getMimeEncoder(64, "\n".getBytes(StandardCharsets.US_ASCII))
                .encodeToString(kp.getPrivate().getEncoded())
            + "\n-----END PRIVATE KEY-----\n";
    Path path = dir.resolve("AuthKey.p8");
    Files.writeString(path, pem);
    return new TestKey(kp.getPublic(), path);
  }

  private static Apns apns(TestKey k, String productionUrl, String sandboxUrl) throws IOException {
    return new Apns(
        new Apns.Config(
            k.path().toString(),
            "KEY123",
            "TEAM456",
            "fi.example.app",
            productionUrl,
            sandboxUrl,
            null));
  }

  private static void verifyJwt(String token, PublicKey pub) throws Exception {
    String[] parts = token.split("\\.");
    assertEquals(3, parts.length);
    byte[] sig = Base64.getUrlDecoder().decode(parts[2]);
    assertEquals(64, sig.length);
    Signature v = Signature.getInstance("SHA256withECDSAinP1363Format");
    v.initVerify(pub);
    v.update((parts[0] + "." + parts[1]).getBytes(StandardCharsets.US_ASCII));
    assertTrue(v.verify(sig));
    JsonNode header = JSON.readTree(Base64.getUrlDecoder().decode(parts[0]));
    assertEquals("ES256", header.get("alg").asText());
    assertEquals("KEY123", header.get("kid").asText());
    JsonNode claims = JSON.readTree(Base64.getUrlDecoder().decode(parts[1]));
    assertEquals("TEAM456", claims.get("iss").asText());
    assertTrue(claims.get("iat").isNumber());
  }

  @Test
  void signsAndAddressesTheRequest() throws Exception {
    TestKey k = testKey();
    String url = serve();
    Apns a = apns(k, url + "/prod", url);
    a.send(
        true,
        "abc123",
        new Apns.Alert(
            "bob", "hello", 3, "42", false, false, Map.of("conversation_id", "42", "seq", 7)),
        TIMEOUT);
    Seen got = seen.getFirst();
    assertEquals("/3/device/abc123", got.path(), "the sandbox URL should be used");
    assertEquals("POST", got.method());
    assertEquals("fi.example.app", got.headers().getFirst("apns-topic"));
    assertEquals("alert", got.headers().getFirst("apns-push-type"));
    assertEquals("10", got.headers().getFirst("apns-priority"));
    verifyJwt(got.headers().getFirst("authorization").replaceFirst("^bearer ", ""), k.pub());
    assertEquals(
        JSON.readTree(
            "{\"aps\":{\"alert\":{\"title\":\"bob\",\"body\":\"hello\"},\"badge\":3,"
                + "\"sound\":\"default\",\"thread-id\":\"42\"},\"conversation_id\":\"42\",\"seq\":7}"),
        got.body());

    a.send(false, "def", new Apns.Alert("x", "", 0, "", false, false, Map.of()), TIMEOUT);
    assertEquals("/prod/3/device/def", seen.get(1).path());
    assertEquals(
        seen.get(0).headers().getFirst("authorization"),
        seen.get(1).headers().getFirst("authorization"),
        "the provider token should be reused");
  }

  @Test
  void sendsBackground() throws Exception {
    String url = serve();
    Apns a = apns(testKey(), url, url);
    a.send(
        false,
        "tok",
        Apns.Alert.background(Map.of("conversation_id", "42", "seq", 7, "deleted", true)),
        TIMEOUT);
    Seen got = seen.getFirst();
    assertEquals("background", got.headers().getFirst("apns-push-type"));
    assertEquals("5", got.headers().getFirst("apns-priority"));
    assertEquals(
        JSON.readTree(
            "{\"aps\":{\"content-available\":1},\"conversation_id\":\"42\",\"seq\":7,"
                + "\"deleted\":true}"),
        got.body());
  }

  @Test
  void reportsUnregisteredTokens() throws Exception {
    TestKey k = testKey();
    String url = serve();
    Apns a = apns(k, url, url);
    Apns.Alert alert = new Apns.Alert("x", "y", 0, "", false, false, Map.of());
    for (var c : List.of(Map.entry(410, "Unregistered"), Map.entry(400, "BadDeviceToken"))) {
      status = c.getKey();
      reply = "{\"reason\":\"" + c.getValue() + "\"}";
      assertThrows(Apns.UnregisteredException.class, () -> a.send(false, "tok", alert, TIMEOUT));
    }
    status = 500;
    reply = "{\"reason\":\"InternalServerError\"}";
    IOException e = assertThrows(IOException.class, () -> a.send(false, "tok", alert, TIMEOUT));
    assertTrue(!(e instanceof Apns.UnregisteredException), e.toString());
  }

  @Test
  void signsANewTokenWhenTheOldOneExpired() throws Exception {
    String url = serve();
    Apns a = apns(testKey(), url, url);
    String first = a.providerToken();
    status = 403;
    reply = "{\"reason\":\"ExpiredProviderToken\"}";
    Apns.Alert alert = new Apns.Alert("x", "y", 0, "", false, false, Map.of());
    assertThrows(IOException.class, () -> a.send(false, "tok", alert, TIMEOUT));
    // ECDSA signatures are randomized, so a token signed anew differs even within the second.
    assertNotEquals(first, a.providerToken());
  }

  @Test
  void readsTheKeyIdFromTheFileName() {
    assertEquals("ABCDE12345", Apns.keyIdFromFileName("/keys/AuthKey_ABCDE12345.p8"));
    assertEquals("", Apns.keyIdFromFileName("/keys/key.p8"));
  }

  @Test
  void refusesAMissingKeyId() throws Exception {
    TestKey k = testKey();
    assertThrows(
        IllegalArgumentException.class,
        () ->
            new Apns(new Apns.Config(k.path().toString(), "", "TEAM", "topic", null, null, null)));
  }
}
