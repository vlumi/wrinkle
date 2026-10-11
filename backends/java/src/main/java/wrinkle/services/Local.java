package wrinkle.services;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ApiError;
import wrinkle.conversation.ConversationService;
import wrinkle.devicesync.DeviceSyncService;
import wrinkle.v1.GetMessagesResponse;
import wrinkle.v1.ListConversationsResponse;
import wrinkle.v1.MarkReadResponse;
import wrinkle.v1.OpenConversationResponse;
import wrinkle.v1.RegisterPushTokenResponse;
import wrinkle.v1.Request;
import wrinkle.v1.Response;
import wrinkle.v1.SendMessageResponse;

/** Runs the conversation and sync services in-process. */
public final class Local implements Backend {
  private static final Logger log = LoggerFactory.getLogger(Local.class);

  private final ConversationService conversations;
  private final DeviceSyncService users;

  public Local(ConversationService conversations, DeviceSyncService users) {
    this.conversations = conversations;
    this.users = users;
  }

  @Override
  public Response call(long userId, long deviceId, Request req) {
    Response.Builder resp = Response.newBuilder().setRequestId(req.getId());
    try {
      dispatch(userId, deviceId, req, resp);
    } catch (Exception e) {
      if (!(e instanceof ApiError)) {
        log.error("request {} failed", req.getId(), e);
      }
      resp.setError(ApiError.toProto(e));
    }
    return resp.build();
  }

  @Override
  public void ack(long userId, long deviceId, long cursor) throws Exception {
    users.ack(userId, deviceId, cursor);
  }

  private void dispatch(long userId, long deviceId, Request req, Response.Builder resp)
      throws Exception {
    if (req.hasConnect()) {
      resp.setConnect(users.connect(req.getConnect()));
      return;
    }
    if (userId == 0) {
      throw ApiError.unauthenticated("connect first");
    }
    switch (req.getBodyCase()) {
      case OPEN_CONVERSATION ->
          resp.setOpenConversation(
              OpenConversationResponse.newBuilder()
                  .setConversation(
                      users.open(userId, req.getOpenConversation().getPeerUserName())));
      case SEND_MESSAGE -> {
        var r = req.getSendMessage();
        resp.setSendMessage(
            SendMessageResponse.newBuilder()
                .setMessage(
                    conversations.send(
                        userId, r.getConversationId(), r.getClientMessageId(), r.getText())));
      }
      case MARK_READ -> {
        var r = req.getMarkRead();
        conversations.markRead(userId, r.getConversationId(), r.getSeq());
        resp.setMarkRead(MarkReadResponse.getDefaultInstance());
      }
      case SYNC ->
          resp.setSync(users.sync(userId, req.getSync().getCursor(), req.getSync().getLimit()));
      case LIST_CONVERSATIONS ->
          resp.setListConversations(
              ListConversationsResponse.newBuilder()
                  .addAllConversations(users.listConversations(userId)));
      case GET_MESSAGES -> {
        var r = req.getGetMessages();
        resp.setGetMessages(
            GetMessagesResponse.newBuilder()
                .addAllMessages(
                    conversations.messages(
                        userId, r.getConversationId(), r.getBeforeSeq(), r.getLimit())));
      }
      case REGISTER_PUSH_TOKEN -> {
        var r = req.getRegisterPushToken();
        users.registerPushToken(userId, deviceId, r.getService(), r.getToken());
        resp.setRegisterPushToken(RegisterPushTokenResponse.getDefaultInstance());
      }
      default -> throw ApiError.invalidArgument("request has no body");
    }
  }
}
