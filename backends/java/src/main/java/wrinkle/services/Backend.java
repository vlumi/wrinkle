package wrinkle.services;

import wrinkle.Uuid;
import wrinkle.v1.Request;
import wrinkle.v1.Response;

/**
 * Handles requests for a device, in-process or in a services process over the internal RPC. The
 * gateway owns the connection state and uses one of the two.
 */
public interface Backend {
  /**
   * Handles one request; the user id is null until the connection has completed Connect. A
   * request's own failure is the response's error body. Throws only when the request could not be
   * handled at all.
   */
  Response call(Uuid userId, long deviceId, Request req) throws Exception;

  /** Records how far a device has processed its user's log. */
  void ack(Uuid userId, long deviceId, long cursor) throws Exception;
}
