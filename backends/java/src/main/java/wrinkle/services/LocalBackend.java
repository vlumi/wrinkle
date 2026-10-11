package wrinkle.services;

import com.google.protobuf.ByteString;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.ApiError;
import wrinkle.Uuid;
import wrinkle.accounts.AccountService;
import wrinkle.accounts.Devices;
import wrinkle.conversation.ConversationKeys;
import wrinkle.conversation.ConversationService;
import wrinkle.conversation.GroupService;
import wrinkle.devicesync.DeviceSyncService;
import wrinkle.uploads.UploadService;
import wrinkle.v1.AddMembersResponse;
import wrinkle.v1.ChangePasswordResponse;
import wrinkle.v1.CreateGroupResponse;
import wrinkle.v1.DeleteAccountResponse;
import wrinkle.v1.DeleteMessageResponse;
import wrinkle.v1.GetDeviceKeysResponse;
import wrinkle.v1.GetMembersResponse;
import wrinkle.v1.GetMessagesResponse;
import wrinkle.v1.LeaveGroupResponse;
import wrinkle.v1.ListConversationsResponse;
import wrinkle.v1.ListDevicesResponse;
import wrinkle.v1.MarkReadResponse;
import wrinkle.v1.OpenConversationResponse;
import wrinkle.v1.PublishKeysResponse;
import wrinkle.v1.RegisterPushTokenResponse;
import wrinkle.v1.RegisterResponse;
import wrinkle.v1.RemoveMemberResponse;
import wrinkle.v1.RenameDeviceResponse;
import wrinkle.v1.Request;
import wrinkle.v1.Response;
import wrinkle.v1.SendMessageResponse;
import wrinkle.v1.SetAdminResponse;
import wrinkle.v1.SetDeviceKeyResponse;
import wrinkle.v1.SetTopicResponse;
import wrinkle.v1.SignInResponse;
import wrinkle.v1.SignOutDeviceResponse;
import wrinkle.v1.SignOutResponse;
import wrinkle.v1.UpdateProfileResponse;

/** Runs the account, conversation and sync services in-process. */
public final class LocalBackend implements Backend {
  private static final Logger log = LoggerFactory.getLogger(LocalBackend.class);

  private final AccountService accounts;
  private final Devices devices;
  private final UploadService uploads;
  private final ConversationService conversations;
  private final GroupService groups;
  private final ConversationKeys keys;
  private final DeviceSyncService users;

  public LocalBackend(
      AccountService accounts,
      Devices devices,
      UploadService uploads,
      ConversationService conversations,
      GroupService groups,
      ConversationKeys keys,
      DeviceSyncService users) {
    this.accounts = accounts;
    this.devices = devices;
    this.uploads = uploads;
    this.conversations = conversations;
    this.groups = groups;
    this.keys = keys;
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
        devices.signOut(userId, deviceId);
        yield resp.setSignOut(SignOutResponse.getDefaultInstance());
      }
      case LIST_DEVICES ->
          resp.setListDevices(ListDevicesResponse.newBuilder().addAllDevices(devices.list(userId)));
      case RENAME_DEVICE -> {
        var r = req.getRenameDevice();
        yield resp.setRenameDevice(
            RenameDeviceResponse.newBuilder()
                .setDevice(devices.rename(userId, r.getDeviceId(), r.getName())));
      }
      case SIGN_OUT_DEVICE -> {
        devices.signOutOther(userId, deviceId, req.getSignOutDevice().getDeviceId());
        yield resp.setSignOutDevice(SignOutDeviceResponse.getDefaultInstance());
      }
      case SET_DEVICE_KEY -> {
        devices.setKey(userId, deviceId, req.getSetDeviceKey().getPublicKey());
        yield resp.setSetDeviceKey(SetDeviceKeyResponse.getDefaultInstance());
      }
      case GET_DEVICE_KEYS ->
          resp.setGetDeviceKeys(
              GetDeviceKeysResponse.newBuilder()
                  .addAllUsers(devices.keys(req.getGetDeviceKeys().getUserIdsList())));
      case DELETE_ACCOUNT -> {
        accounts.deleteAccount(userId, req.getDeleteAccount(), () -> groups.leaveAll(userId));
        yield resp.setDeleteAccount(DeleteAccountResponse.getDefaultInstance());
      }
      case CREATE_UPLOAD -> resp.setCreateUpload(uploads.create(userId, req.getCreateUpload()));
      case OPEN_CONVERSATION ->
          resp.setOpenConversation(
              OpenConversationResponse.newBuilder()
                  .setConversation(users.open(userId, req.getOpenConversation().getPeerName())));
      case CREATE_GROUP ->
          resp.setCreateGroup(
              CreateGroupResponse.newBuilder()
                  .setConversation(groups.create(userId, req.getCreateGroup())));
      case ADD_MEMBERS -> {
        var r = req.getAddMembers();
        groups.addMembers(userId, conversationId(r.getConversationId()), r.getMemberNamesList());
        yield resp.setAddMembers(AddMembersResponse.getDefaultInstance());
      }
      case REMOVE_MEMBER -> {
        var r = req.getRemoveMember();
        groups.removeMember(userId, conversationId(r.getConversationId()), userIdOf(r.getUserId()));
        yield resp.setRemoveMember(RemoveMemberResponse.getDefaultInstance());
      }
      case LEAVE_GROUP -> {
        groups.leave(userId, conversationId(req.getLeaveGroup().getConversationId()));
        yield resp.setLeaveGroup(LeaveGroupResponse.getDefaultInstance());
      }
      case SET_TOPIC -> {
        var r = req.getSetTopic();
        groups.setTopic(userId, conversationId(r.getConversationId()), r.getTopic());
        yield resp.setSetTopic(SetTopicResponse.getDefaultInstance());
      }
      case SET_ADMIN -> {
        var r = req.getSetAdmin();
        groups.setAdmin(
            userId, conversationId(r.getConversationId()), userIdOf(r.getUserId()), r.getAdmin());
        yield resp.setSetAdmin(SetAdminResponse.getDefaultInstance());
      }
      case GET_MEMBERS ->
          resp.setGetMembers(
              GetMembersResponse.newBuilder()
                  .addAllMembers(
                      groups.members(
                          userId, conversationId(req.getGetMembers().getConversationId()))));
      case SEND_MESSAGE -> {
        var r = req.getSendMessage();
        yield resp.setSendMessage(
            SendMessageResponse.newBuilder()
                .setMessage(
                    conversations.send(
                        userId,
                        conversationId(r.getConversationId()),
                        r.getClientMessageId(),
                        r.getSealed())));
      }
      case PUBLISH_KEYS -> {
        var r = req.getPublishKeys();
        keys.publish(
            userId,
            deviceId,
            conversationId(r.getConversationId()),
            Integer.toUnsignedLong(r.getEpoch()),
            r.getKeysList());
        yield resp.setPublishKeys(PublishKeysResponse.getDefaultInstance());
      }
      case GET_CONVERSATION_KEYS -> {
        var r = req.getGetConversationKeys();
        yield resp.setGetConversationKeys(
            keys.get(
                userId,
                deviceId,
                conversationId(r.getConversationId()),
                Integer.toUnsignedLong(r.getAfterEpoch())));
      }
      case DELETE_MESSAGE -> {
        var r = req.getDeleteMessage();
        conversations.deleteMessage(userId, conversationId(r.getConversationId()), r.getSeq());
        yield resp.setDeleteMessage(DeleteMessageResponse.getDefaultInstance());
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

  private static Uuid userIdOf(ByteString b) {
    Uuid id = Uuid.parse(b);
    if (id == null) {
      throw ApiError.invalidArgument("user_id must be 16 bytes");
    }
    return id;
  }

  private static Uuid conversationId(ByteString b) {
    Uuid id = Uuid.parse(b);
    if (id == null) {
      throw ApiError.invalidArgument("conversation_id must be 16 bytes");
    }
    return id;
  }
}
