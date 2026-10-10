package wrinkle.gateway;

import com.google.protobuf.CodedOutputStream;
import com.google.protobuf.InvalidProtocolBufferException;
import com.linecorp.armeria.common.websocket.WebSocketCloseStatus;
import com.linecorp.armeria.common.websocket.WebSocketFrame;
import com.linecorp.armeria.common.websocket.WebSocketFrameType;
import com.linecorp.armeria.common.websocket.WebSocketWriter;
import java.io.IOException;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ApiError;
import wrinkle.Metrics;
import wrinkle.v1.ConnectRequest;
import wrinkle.v1.ConnectResponse;
import wrinkle.v1.ErrorCode;
import wrinkle.v1.Frame;
import wrinkle.v1.GetMessagesResponse;
import wrinkle.v1.ListConversationsResponse;
import wrinkle.v1.MarkReadResponse;
import wrinkle.v1.OpenConversationResponse;
import wrinkle.v1.Pong;
import wrinkle.v1.Request;
import wrinkle.v1.Response;
import wrinkle.v1.SendMessageResponse;

/** One device's WebSocket connection. */
final class Conn {
  private static final Logger log = LoggerFactory.getLogger(Conn.class);

  private final Gateway gateway;
  private final WebSocketWriter out;
  private final String remote;
  private volatile boolean connected;
  private volatile long userId;
  private volatile long deviceId;

  Conn(Gateway gateway, WebSocketWriter out, String remote) {
    this.gateway = gateway;
    this.out = out;
    this.remote = remote;
  }

  boolean isConnected() {
    return connected;
  }

  long userId() {
    return userId;
  }

  long deviceId() {
    return deviceId;
  }

  void receive(WebSocketFrame frame) {
    if (frame.type() != WebSocketFrameType.BINARY) {
      if (frame.type() == WebSocketFrameType.TEXT) {
        out.close(WebSocketCloseStatus.INVALID_MESSAGE_TYPE, "frames are binary protobuf");
      }
      return;
    }
    Frame f;
    try {
      f = Frame.parseFrom(frame.array());
    } catch (InvalidProtocolBufferException e) {
      out.close(WebSocketCloseStatus.POLICY_VIOLATION, "malformed frame");
      return;
    }
    switch (f.getKindCase()) {
      case REQUEST -> Metrics.virtualThreads().execute(() -> handleRequest(f.getRequest()));
      case ACK -> {
        if (connected) {
          long cursor = f.getAck().getCursor();
          Metrics.virtualThreads()
              .execute(
                  () -> {
                    try {
                      gateway.users.ack(userId, deviceId, cursor);
                    } catch (Exception e) {
                      log.warn("ack failed", e);
                    }
                  });
        }
      }
      case PING ->
          send(Frame.newBuilder().setPong(Pong.getDefaultInstance()).build().toByteArray());
      case PONG -> {}
      default -> out.close(WebSocketCloseStatus.POLICY_VIOLATION, "unexpected frame");
    }
  }

  /** Queues bytes for the client. A closed writer drops them; the device syncs on reconnect. */
  void send(byte[] data) {
    out.tryWrite(WebSocketFrame.ofBinary(data));
  }

  static byte[] eventFrame(byte[] payload) {
    int tag = CodedOutputStream.computeTagSize(3);
    int len = CodedOutputStream.computeUInt32SizeNoTag(payload.length);
    byte[] data = new byte[tag + len + payload.length];
    CodedOutputStream cos = CodedOutputStream.newInstance(data);
    try {
      cos.writeTag(3, 2);
      cos.writeUInt32NoTag(payload.length);
      cos.writeRawBytes(payload);
      cos.flush();
    } catch (IOException e) {
      throw new IllegalStateException(e);
    }
    return data;
  }

  void push(byte[] payload) {
    send(eventFrame(payload));
    Metrics.EVENTS_PUSHED.inc();
  }

  void closed() {
    out.close();
  }

  void close() {
    out.close(WebSocketCloseStatus.SERVICE_RESTART, "shutting down");
  }

  private void handleRequest(Request req) {
    long start = System.nanoTime();
    Response.Builder resp = Response.newBuilder().setRequestId(req.getId());
    String code = "ok";
    try {
      dispatch(req, resp);
    } catch (Exception e) {
      if (!(e instanceof ApiError)) {
        log.error("request {} failed", req.getId(), e);
      }
      resp.setError(ApiError.toProto(e));
      code = resp.getError().getCode().name();
    }
    send(Frame.newBuilder().setResponse(resp).build().toByteArray());
    String type = requestType(req);
    Metrics.REQUESTS.labelValues(type, code).inc();
    Metrics.REQUEST_DURATION.labelValues(type).observe((System.nanoTime() - start) / 1e9);
  }

  private static String requestType(Request req) {
    return switch (req.getBodyCase()) {
      case CONNECT -> "connect";
      case OPEN_CONVERSATION -> "open_conversation";
      case SEND_MESSAGE -> "send_message";
      case MARK_READ -> "mark_read";
      case SYNC -> "sync";
      case LIST_CONVERSATIONS -> "list_conversations";
      case GET_MESSAGES -> "get_messages";
      default -> "unknown";
    };
  }

  private void dispatch(Request req, Response.Builder resp) throws Exception {
    if (req.hasConnect()) {
      connect(req.getConnect(), resp);
      return;
    }
    if (!connected) {
      throw ApiError.unauthenticated("connect first");
    }
    switch (req.getBodyCase()) {
      case OPEN_CONVERSATION ->
          resp.setOpenConversation(
              OpenConversationResponse.newBuilder()
                  .setConversation(
                      gateway.users.open(userId, req.getOpenConversation().getPeerUserName())));
      case SEND_MESSAGE -> {
        var r = req.getSendMessage();
        resp.setSendMessage(
            SendMessageResponse.newBuilder()
                .setMessage(
                    gateway.conversations.send(
                        userId, r.getConversationId(), r.getClientMessageId(), r.getText())));
      }
      case MARK_READ -> {
        var r = req.getMarkRead();
        gateway.conversations.markRead(userId, r.getConversationId(), r.getSeq());
        resp.setMarkRead(MarkReadResponse.getDefaultInstance());
      }
      case SYNC ->
          resp.setSync(
              gateway.users.sync(userId, req.getSync().getCursor(), req.getSync().getLimit()));
      case LIST_CONVERSATIONS ->
          resp.setListConversations(
              ListConversationsResponse.newBuilder()
                  .addAllConversations(gateway.users.listConversations(userId)));
      case GET_MESSAGES -> {
        var r = req.getGetMessages();
        resp.setGetMessages(
            GetMessagesResponse.newBuilder()
                .addAllMessages(
                    gateway.conversations.messages(
                        userId, r.getConversationId(), r.getBeforeSeq(), r.getLimit())));
      }
      default -> throw ApiError.invalidArgument("request has no body");
    }
  }

  private void connect(ConnectRequest req, Response.Builder resp) throws Exception {
    if (connected) {
      throw ApiError.failedPrecondition("already connected");
    }
    ConnectResponse connectResponse = gateway.users.connect(req);
    synchronized (this) {
      if (connected) {
        throw ApiError.failedPrecondition("already connected");
      }
      userId = connectResponse.getUser().getUserId();
      deviceId = connectResponse.getDeviceId();
      connected = true;
    }
    gateway.attach(this);
    resp.setConnect(connectResponse);
  }

  @Override
  public String toString() {
    return "conn " + remote + " user " + userId + " device " + deviceId;
  }

  static ErrorCode codeOf(Throwable t) {
    return t instanceof ApiError e ? e.code() : ErrorCode.ERROR_CODE_INTERNAL;
  }
}
