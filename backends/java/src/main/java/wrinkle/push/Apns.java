package wrinkle.push;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.PrivateKey;
import java.security.Signature;
import java.security.interfaces.ECPrivateKey;
import java.security.spec.PKCS8EncodedKeySpec;
import java.time.Duration;
import java.time.Instant;
import java.util.Base64;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** APNs with a token-based provider, over HTTP/2. */
public final class Apns {
  // APNs endpoints; the tests point both at a local server.
  private static final String PRODUCTION_URL = "https://api.push.apple.com";
  private static final String SANDBOX_URL = "https://api.sandbox.push.apple.com";
  // Apple accepts a provider token for an hour and rejects one refreshed more often than every 20
  // minutes.
  private static final Duration TOKEN_LIFETIME = Duration.ofMinutes(45);
  private static final Duration REQUEST_TIMEOUT = Duration.ofSeconds(10);
  private static final Pattern KEY_ID_IN_FILE_NAME =
      Pattern.compile("AuthKey_([A-Z0-9]{10})\\.p8$");
  private static final ObjectMapper JSON = new ObjectMapper();
  private static final Base64.Encoder B64URL = Base64.getUrlEncoder().withoutPadding();

  /** The most a notification's JSON may have. */
  static final int MAX_PAYLOAD = 4096;

  /**
   * A token-based provider: a .p8 key from the developer account, its key id, the team id, and the
   * app's bundle id as the topic. Without a key id, the one in the key's file name is used; null
   * URLs and client take the defaults.
   */
  public record Config(
      String keyFile,
      String keyId,
      String teamId,
      String topic,
      String productionUrl,
      String sandboxUrl,
      HttpClient http) {}

  /** The token is no longer valid for the topic; the registration should be removed. */
  public static final class UnregisteredException extends IOException {
    private static final long serialVersionUID = 1L;

    UnregisteredException(String reason) {
      super("device token is no longer registered: " + reason);
    }
  }

  /**
   * What a notification shows, or, when background, a silent notification that wakes the app to act
   * on data. With mutable content, the app's notification service extension may change the alert
   * before it shows, as it does to decrypt the message. Data is custom keys beside "aps", for the
   * app to act on when the alert is tapped or the app woken.
   */
  public record Alert(
      String title,
      String body,
      int badge,
      String threadId,
      boolean background,
      boolean mutableContent,
      Map<String, Object> data) {

    /** A silent notification with only the data. */
    static Alert background(Map<String, Object> data) {
      return new Alert("", "", 0, "", true, false, data);
    }

    /** The notification's JSON. */
    byte[] payload() {
      Map<String, Object> aps = new LinkedHashMap<>();
      if (background) {
        aps.put("content-available", 1);
      } else {
        Map<String, Object> shown = new LinkedHashMap<>();
        shown.put("title", title);
        shown.put("body", body);
        aps.put("alert", shown);
        aps.put("badge", badge);
        aps.put("sound", "default");
        aps.put("thread-id", threadId);
        if (mutableContent) {
          aps.put("mutable-content", 1);
        }
      }
      Map<String, Object> payload = new LinkedHashMap<>();
      payload.put("aps", aps);
      payload.putAll(data);
      try {
        return JSON.writeValueAsBytes(payload);
      } catch (JsonProcessingException e) {
        throw new IllegalArgumentException(e);
      }
    }
  }

  private final PrivateKey key;
  private final String keyId;
  private final String teamId;
  private final String topic;
  private final String productionUrl;
  private final String sandboxUrl;
  private final HttpClient http;
  private String token = "";
  private Instant tokenTime = Instant.EPOCH;

  public Apns(Config cfg) throws IOException {
    this.key = readKey(Path.of(cfg.keyFile()));
    String id =
        cfg.keyId() == null || cfg.keyId().isEmpty()
            ? keyIdFromFileName(cfg.keyFile())
            : cfg.keyId();
    if (id.isEmpty() || isEmpty(cfg.teamId()) || isEmpty(cfg.topic())) {
      throw new IllegalArgumentException("apns: key id, team id and topic are all required");
    }
    this.keyId = id;
    this.teamId = cfg.teamId();
    this.topic = cfg.topic();
    this.productionUrl = isEmpty(cfg.productionUrl()) ? PRODUCTION_URL : cfg.productionUrl();
    this.sandboxUrl = isEmpty(cfg.sandboxUrl()) ? SANDBOX_URL : cfg.sandboxUrl();
    this.http =
        cfg.http() != null
            ? cfg.http()
            : HttpClient.newBuilder()
                .version(HttpClient.Version.HTTP_2)
                .connectTimeout(REQUEST_TIMEOUT)
                .build();
  }

  private static boolean isEmpty(String s) {
    return s == null || s.isEmpty();
  }

  private static PrivateKey readKey(Path file) throws IOException {
    String pem = Files.readString(file, StandardCharsets.US_ASCII);
    int begin = pem.indexOf("-----BEGIN PRIVATE KEY-----");
    int end = pem.indexOf("-----END PRIVATE KEY-----");
    if (begin < 0 || end < begin) {
      throw new IOException("apns key: not a PEM private key");
    }
    String body = pem.substring(begin + "-----BEGIN PRIVATE KEY-----".length(), end);
    try {
      PrivateKey k =
          KeyFactory.getInstance("EC")
              .generatePrivate(new PKCS8EncodedKeySpec(Base64.getMimeDecoder().decode(body)));
      if (!(k instanceof ECPrivateKey)) {
        throw new IOException("apns key: not an EC key");
      }
      return k;
    } catch (GeneralSecurityException | IllegalArgumentException e) {
      throw new IOException("apns key: " + e.getMessage(), e);
    }
  }

  /** The key id in the name Apple gives the download, AuthKey_<id>.p8, or empty. */
  static String keyIdFromFileName(String path) {
    Matcher m = KEY_ID_IN_FILE_NAME.matcher(path);
    return m.find() ? m.group(1) : "";
  }

  /** The current JWT, signing a new one when it is old. */
  synchronized String providerToken() throws GeneralSecurityException {
    Instant now = Instant.now();
    if (!token.isEmpty() && Duration.between(tokenTime, now).compareTo(TOKEN_LIFETIME) < 0) {
      return token;
    }
    String header = "{\"alg\":\"ES256\",\"kid\":\"" + keyId + "\"}";
    String claims = "{\"iss\":\"" + teamId + "\",\"iat\":" + now.getEpochSecond() + "}";
    String signingInput =
        B64URL.encodeToString(header.getBytes(StandardCharsets.UTF_8))
            + "."
            + B64URL.encodeToString(claims.getBytes(StandardCharsets.UTF_8));
    // JWS wants the raw 64-byte r || s, not ASN.1.
    Signature s = Signature.getInstance("SHA256withECDSAinP1363Format");
    s.initSign(key);
    s.update(signingInput.getBytes(StandardCharsets.US_ASCII));
    token = signingInput + "." + B64URL.encodeToString(s.sign());
    tokenTime = now;
    return token;
  }

  private synchronized void dropToken() {
    token = "";
  }

  /** Delivers one alert to one device token, within the timeout. */
  public void send(boolean sandbox, String deviceToken, Alert alert, Duration timeout)
      throws IOException, InterruptedException {
    String jwt;
    try {
      jwt = providerToken();
    } catch (GeneralSecurityException e) {
      throw new IOException("apns: signing the provider token: " + e.getMessage(), e);
    }
    // Apple requires the low priority for background notifications.
    String pushType = alert.background() ? "background" : "alert";
    String priority = alert.background() ? "5" : "10";
    String base = sandbox ? sandboxUrl : productionUrl;
    HttpRequest req =
        HttpRequest.newBuilder(URI.create(base + "/3/device/" + deviceToken))
            .timeout(timeout)
            .header("authorization", "bearer " + jwt)
            .header("apns-topic", topic)
            .header("apns-push-type", pushType)
            .header("apns-priority", priority)
            .POST(HttpRequest.BodyPublishers.ofByteArray(alert.payload()))
            .build();
    HttpResponse<byte[]> resp = http.send(req, HttpResponse.BodyHandlers.ofByteArray());
    if (resp.statusCode() == 200) {
      return;
    }
    String reason = "";
    try {
      JsonNode r = JSON.readTree(resp.body()).get("reason");
      if (r != null && r.isTextual()) {
        reason = r.asText();
      }
    } catch (IOException | RuntimeException e) {
      // No reason given.
    }
    if (resp.statusCode() == 410
        || reason.equals("BadDeviceToken")
        || reason.equals("DeviceTokenNotForTopic")) {
      throw new UnregisteredException(reason);
    }
    if (reason.equals("ExpiredProviderToken")) {
      dropToken();
    }
    throw new IOException("apns: " + resp.statusCode() + " " + reason);
  }
}
