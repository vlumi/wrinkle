package wrinkle.uploads;

import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.MessageDigest;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.util.HexFormat;
import java.util.Locale;
import java.util.Map;
import java.util.TreeMap;
import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;

/**
 * Presigned S3 URLs (AWS Signature Version 4, query parameters), for the object storage behind the
 * blob address. Signatures are computed for the host the blob proxy sets on every request.
 */
public final class Presigner {
  // The blob proxy sets this Host on every request it forwards.
  static final String HOST = "blobs";
  private static final String ALGORITHM = "AWS4-HMAC-SHA256";
  private static final DateTimeFormatter DATE =
      DateTimeFormatter.ofPattern("yyyyMMdd", Locale.ROOT).withZone(ZoneOffset.UTC);
  private static final DateTimeFormatter AMZ_DATE =
      DateTimeFormatter.ofPattern("yyyyMMdd'T'HHmmss'Z'", Locale.ROOT).withZone(ZoneOffset.UTC);
  private static final HexFormat HEX = HexFormat.of();

  private final String keyId;
  private final String secret;
  private final String region;

  public Presigner(String keyId, String secret, String region) {
    this.keyId = keyId;
    this.secret = secret;
    this.region = region;
  }

  /**
   * The path and query that let the holder send this request until the expiry: method and path as
   * given, with exactly the headers named (names lowercased).
   */
  public String presign(
      String method, String path, Map<String, String> signedHeaders, Instant t, long expires) {
    String date = DATE.format(t);
    String amzDate = AMZ_DATE.format(t);
    String scope = date + "/" + region + "/s3/aws4_request";
    TreeMap<String, String> headers = new TreeMap<>();
    headers.put("host", HOST);
    signedHeaders.forEach((k, v) -> headers.put(k.toLowerCase(Locale.ROOT), v));
    String signed = String.join(";", headers.keySet());

    TreeMap<String, String> params = new TreeMap<>();
    params.put("X-Amz-Algorithm", ALGORITHM);
    params.put("X-Amz-Credential", keyId + "/" + scope);
    params.put("X-Amz-Date", amzDate);
    params.put("X-Amz-Expires", Long.toString(expires));
    params.put("X-Amz-SignedHeaders", signed);
    StringBuilder query = new StringBuilder();
    params.forEach(
        (k, v) -> {
          if (!query.isEmpty()) {
            query.append('&');
          }
          query.append(encode(k)).append('=').append(encode(v));
        });

    StringBuilder canonical = new StringBuilder();
    canonical.append(method).append('\n').append(path).append('\n').append(query).append('\n');
    headers.forEach((k, v) -> canonical.append(k).append(':').append(v.strip()).append('\n'));
    canonical.append('\n').append(signed).append('\n').append("UNSIGNED-PAYLOAD");

    String toSign =
        ALGORITHM + "\n" + amzDate + "\n" + scope + "\n" + HEX.formatHex(sha256(canonical));
    byte[] key = hmac(("AWS4" + secret).getBytes(StandardCharsets.UTF_8), date);
    key = hmac(key, region);
    key = hmac(key, "s3");
    key = hmac(key, "aws4_request");
    String signature = HEX.formatHex(hmac(key, toSign));
    return path + "?" + query + "&X-Amz-Signature=" + signature;
  }

  /** URI encoding as SigV4 wants it: unreserved characters kept, every other byte %XX. */
  static String encode(String s) {
    StringBuilder b = new StringBuilder();
    for (byte c : s.getBytes(StandardCharsets.UTF_8)) {
      if ((c >= 'A' && c <= 'Z')
          || (c >= 'a' && c <= 'z')
          || (c >= '0' && c <= '9')
          || c == '-'
          || c == '_'
          || c == '.'
          || c == '~') {
        b.append((char) c);
      } else {
        b.append('%').append(HEX.withUpperCase().toHexDigits(c));
      }
    }
    return b.toString();
  }

  private static byte[] sha256(CharSequence s) {
    try {
      return MessageDigest.getInstance("SHA-256")
          .digest(s.toString().getBytes(StandardCharsets.UTF_8));
    } catch (GeneralSecurityException e) {
      throw new IllegalStateException(e);
    }
  }

  private static byte[] hmac(byte[] key, String data) {
    try {
      Mac mac = Mac.getInstance("HmacSHA256");
      mac.init(new SecretKeySpec(key, "HmacSHA256"));
      return mac.doFinal(data.getBytes(StandardCharsets.UTF_8));
    } catch (GeneralSecurityException e) {
      throw new IllegalStateException(e);
    }
  }
}
