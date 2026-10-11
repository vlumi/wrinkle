package wrinkle.services;

import com.google.protobuf.ByteString;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ApiError;
import wrinkle.Uuid;
import wrinkle.accounts.AccountService;
import wrinkle.conversation.ConversationService;
import wrinkle.devicesync.DeviceSyncService;
import wrinkle.v1.ChangePasswordResponse;
import wrinkle.v1.GetMessagesResponse;
import wrinkle.v1.ListConversationsResponse;
import wrinkle.v1.MarkReadResponse;
import wrinkle.v1.OpenConversationResponse;
import wrinkle.v1.RegisterPushTokenResponse;
import wrinkle.v1.RegisterResponse;
import wrinkle.v1.Request;
import wrinkle.v1.Response;
import wrinkle.v1.SendMessageResponse;
import wrinkle.v1.SignInResponse;
import wrinkle.v1.SignOutResponse;
import wrinkle.v1.UpdateProfileResponse;

/** Runs the account, conversation and sync services in-process. */
public final class LocalBackend implements Backend {
  private static final Logger log = LoggerFactory.getLogger(LocalBackend.class);

  private final AccountService accounts;
  private final ConversationService conversations;
  private final DeviceSyncService users;

  public LocalBackend(
      AccountService accounts, ConversationService conversations, DeviceSyncService users) {
    this.accounts = accounts;
    this.conversations = conversations;
    this.users = users;
  }

  @Override
  public Response call(Uuid userId, long deviceId, Request req) {
    try {
      return dispatch(userId, deviceId, req).setRequestId(req.getId()).build();
    } catch (Exception e) {
      if (!(e instanceof ApiError)) {
        log.error("request {} failed", req.getId(), e);
      }
      return ApiError.response(req, e);
    }
  }

  @Override
  public void ack(Uuid userId, long deviceId, long cursor) throws Exception {
    users.ack(userId, deviceId, cursor);
  }

  private Response.Builder dispatch(Uuid userId, long deviceId, Request req) throws Exception {
    Response.Builder resp = Response.newBuilder();
    switch (req.getBodyCase()) {
      case REGISTER, SIGN_IN, CONNECT -> {
        if (userId != null) {
          throw ApiError.failedPrecondition("already connected");
        }
        return signIn(req, resp);
      }
      case BODY_NOT_SET -> throw ApiError.invalidArgument("request has no body");
      default -> {}
    }
    if (userId == null) {
      throw ApiError.unauthenticated("connect first");
    }
    return switch (req.getBodyCase()) {
      case CHANGE_PASSWORD -> {
        accounts.changePassword(userId, deviceId, req.getChangePassword());
        yield resp.setChangePassword(ChangePasswordResponse.getDefaultInstance());
      }
      case UPDATE_PROFILE ->
          resp.setUpdateProfile(
              UpdateProfileResponse.newBuilder()
                  .setUser(accounts.updateProfile(userId, req.getUpdateProfile())));
      case SIGN_OUT -> {
        accounts.signOut(userId, deviceId);
        yield resp.setSignOut(SignOutResponse.getDefaultInstance());
      }
      case OPEN_CONVERSATION ->
          resp.setOpenConversation(
              OpenConversationResponse.newBuilder()
                  .setConversation(users.open(userId, req.getOpenConversation().getPeerName())));
      case SEND_MESSAGE -> {
        var r = req.getSendMessage();
        yield resp.setSendMessage(
            SendMessageResponse.newBuilder()
                .setMessage(
                    conversations.send(
                        userId,
                        conversationId(r.getConversationId()),
                        r.getClientMessageId(),
                        r.getText())));
      }
      case MARK_READ -> {
        var r = req.getMarkRead();
        conversations.markRead(userId, conversationId(r.getConversationId()), r.getSeq());
        yield resp.setMarkRead(MarkReadResponse.getDefaultInstance());
      }
      case SYNC ->
          resp.setSync(users.sync(userId, req.getSync().getCursor(), req.getSync().getLimit()));
      case LIST_CONVERSATIONS ->
          resp.setListConversations(
              ListConversationsResponse.newBuilder()
                  .addAllConversations(users.listConversations(userId)));
      case GET_MESSAGES -> {
        var r = req.getGetMessages();
        yield resp.setGetMessages(
            GetMessagesResponse.newBuilder()
                .addAllMessages(
                    conversations.messages(
                        userId,
                        conversationId(r.getConversationId()),
                        r.getBeforeSeq(),
                        r.getLimit())));
      }
      case REGISTER_PUSH_TOKEN -> {
        var r = req.getRegisterPushToken();
        users.registerPushToken(userId, deviceId, r.getService(), r.getToken());
        yield resp.setRegisterPushToken(RegisterPushTokenResponse.getDefaultInstance());
      }
      default -> throw ApiError.invalidArgument("unknown request");
    };
  }

  /** The requests allowed before Connect. */
  private Response.Builder signIn(Request req, Response.Builder resp) throws Exception {
    return switch (req.getBodyCase()) {
      case REGISTER ->
          resp.setRegister(
              RegisterResponse.newBuilder().setCredentials(accounts.register(req.getRegister())));
      case SIGN_IN ->
          resp.setSignIn(
              SignInResponse.newBuilder().setCredentials(accounts.signIn(req.getSignIn())));
      default -> resp.setConnect(accounts.connect(req.getConnect()));
    };
  }

  private static Uuid conversationId(ByteString b) {
    Uuid id = Uuid.parse(b);
    if (id == null) {
      throw ApiError.invalidArgument("conversation_id must be 16 bytes");
    }
    return id;
  }
}
