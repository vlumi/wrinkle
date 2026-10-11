package wrinkle.uploads;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import wrinkle.Uuid;

/** The object storage behind the blob address: blobs by id under /blobs/, as the proxy serves. */
public final class Blobs {
  private static final Duration TIMEOUT = Duration.ofSeconds(5);
  private static final long DELETE_EXPIRES_SECONDS = 60;

  private final String url;
  private final Presigner presigner;
  private final HttpClient http =
      HttpClient.newBuilder().connectTimeout(TIMEOUT).version(HttpClient.Version.HTTP_1_1).build();

  /** The blob proxy's base URL, without a trailing slash, and the storage credentials. */
  public Blobs(String url, Presigner presigner) {
    this.url = url.endsWith("/") ? url.substring(0, url.length() - 1) : url;
    this.presigner = presigner;
  }

  /** The path a blob is read and written at. */
  public static String path(Uuid blobId) {
    return "/blobs/" + blobId.hex();
  }

  /** A presigned path for a PUT of exactly this media type and size. */
  public String presignPut(Uuid blobId, String mediaType, long size, Instant t, long expires) {
    return presigner.presign(
        "PUT",
        path(blobId),
        Map.of("content-type", mediaType, "content-length", Long.toString(size)),
        t,
        expires);
  }

  /** Whether the blob has been uploaded. */
  public boolean exists(Uuid blobId) throws IOException, InterruptedException {
    HttpRequest req =
        HttpRequest.newBuilder(URI.create(url + path(blobId)))
            .timeout(TIMEOUT)
            .method("HEAD", HttpRequest.BodyPublishers.noBody())
            .build();
    return http.send(req, HttpResponse.BodyHandlers.discarding()).statusCode() == 200;
  }

  /** Deletes the blob; returns the HTTP status (200 or 204, or 404 when there was none). */
  public int delete(Uuid blobId) throws IOException, InterruptedException {
    String signed =
        presigner.presign("DELETE", path(blobId), Map.of(), Instant.now(), DELETE_EXPIRES_SECONDS);
    HttpRequest req =
        HttpRequest.newBuilder(URI.create(url + signed)).timeout(TIMEOUT).DELETE().build();
    return http.send(req, HttpResponse.BodyHandlers.discarding()).statusCode();
  }
}
