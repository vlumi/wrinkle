defmodule Wrinkle.V1.ErrorCode do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "wrinkle.v1.ErrorCode",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :ERROR_CODE_UNSPECIFIED, 0
  field :ERROR_CODE_INVALID_ARGUMENT, 1
  field :ERROR_CODE_UNAUTHENTICATED, 2
  field :ERROR_CODE_PERMISSION_DENIED, 3
  field :ERROR_CODE_NOT_FOUND, 4
  field :ERROR_CODE_FAILED_PRECONDITION, 5
  field :ERROR_CODE_UNAVAILABLE, 6
  field :ERROR_CODE_INTERNAL, 7
  field :ERROR_CODE_RESOURCE_EXHAUSTED, 8
  field :ERROR_CODE_ALREADY_EXISTS, 9
end

defmodule Wrinkle.V1.Frame do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Frame",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :kind, 0

  field :request, 1, type: Wrinkle.V1.Request, oneof: 0
  field :response, 2, type: Wrinkle.V1.Response, oneof: 0
  field :event, 3, type: Wrinkle.V1.Event, oneof: 0
  field :ack, 4, type: Wrinkle.V1.Ack, oneof: 0
  field :ping, 5, type: Wrinkle.V1.Ping, oneof: 0
  field :pong, 6, type: Wrinkle.V1.Pong, oneof: 0
end

defmodule Wrinkle.V1.Request do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Request",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :body, 0

  field :id, 1, type: :uint64
  field :connect, 10, type: Wrinkle.V1.ConnectRequest, oneof: 0

  field :open_conversation, 11,
    type: Wrinkle.V1.OpenConversationRequest,
    json_name: "openConversation",
    oneof: 0

  field :send_message, 12, type: Wrinkle.V1.SendMessageRequest, json_name: "sendMessage", oneof: 0
  field :mark_read, 13, type: Wrinkle.V1.MarkReadRequest, json_name: "markRead", oneof: 0
  field :sync, 14, type: Wrinkle.V1.SyncRequest, oneof: 0

  field :list_conversations, 15,
    type: Wrinkle.V1.ListConversationsRequest,
    json_name: "listConversations",
    oneof: 0

  field :get_messages, 16, type: Wrinkle.V1.GetMessagesRequest, json_name: "getMessages", oneof: 0

  field :register_push_token, 17,
    type: Wrinkle.V1.RegisterPushTokenRequest,
    json_name: "registerPushToken",
    oneof: 0

  field :register, 18, type: Wrinkle.V1.RegisterRequest, oneof: 0
  field :sign_in, 19, type: Wrinkle.V1.SignInRequest, json_name: "signIn", oneof: 0

  field :change_password, 20,
    type: Wrinkle.V1.ChangePasswordRequest,
    json_name: "changePassword",
    oneof: 0

  field :update_profile, 21,
    type: Wrinkle.V1.UpdateProfileRequest,
    json_name: "updateProfile",
    oneof: 0

  field :sign_out, 22, type: Wrinkle.V1.SignOutRequest, json_name: "signOut", oneof: 0
  field :list_devices, 23, type: Wrinkle.V1.ListDevicesRequest, json_name: "listDevices", oneof: 0

  field :rename_device, 24,
    type: Wrinkle.V1.RenameDeviceRequest,
    json_name: "renameDevice",
    oneof: 0

  field :sign_out_device, 25,
    type: Wrinkle.V1.SignOutDeviceRequest,
    json_name: "signOutDevice",
    oneof: 0

  field :delete_account, 26,
    type: Wrinkle.V1.DeleteAccountRequest,
    json_name: "deleteAccount",
    oneof: 0

  field :create_upload, 27,
    type: Wrinkle.V1.CreateUploadRequest,
    json_name: "createUpload",
    oneof: 0

  field :create_group, 28, type: Wrinkle.V1.CreateGroupRequest, json_name: "createGroup", oneof: 0
  field :add_members, 29, type: Wrinkle.V1.AddMembersRequest, json_name: "addMembers", oneof: 0

  field :remove_member, 30,
    type: Wrinkle.V1.RemoveMemberRequest,
    json_name: "removeMember",
    oneof: 0

  field :leave_group, 31, type: Wrinkle.V1.LeaveGroupRequest, json_name: "leaveGroup", oneof: 0
  field :set_topic, 32, type: Wrinkle.V1.SetTopicRequest, json_name: "setTopic", oneof: 0
  field :set_admin, 33, type: Wrinkle.V1.SetAdminRequest, json_name: "setAdmin", oneof: 0
  field :get_members, 34, type: Wrinkle.V1.GetMembersRequest, json_name: "getMembers", oneof: 0
end

defmodule Wrinkle.V1.Response do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Response",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :body, 0

  field :request_id, 1, type: :uint64, json_name: "requestId"
  field :error, 2, type: Wrinkle.V1.Error, oneof: 0
  field :connect, 10, type: Wrinkle.V1.ConnectResponse, oneof: 0

  field :open_conversation, 11,
    type: Wrinkle.V1.OpenConversationResponse,
    json_name: "openConversation",
    oneof: 0

  field :send_message, 12,
    type: Wrinkle.V1.SendMessageResponse,
    json_name: "sendMessage",
    oneof: 0

  field :mark_read, 13, type: Wrinkle.V1.MarkReadResponse, json_name: "markRead", oneof: 0
  field :sync, 14, type: Wrinkle.V1.SyncResponse, oneof: 0

  field :list_conversations, 15,
    type: Wrinkle.V1.ListConversationsResponse,
    json_name: "listConversations",
    oneof: 0

  field :get_messages, 16,
    type: Wrinkle.V1.GetMessagesResponse,
    json_name: "getMessages",
    oneof: 0

  field :register_push_token, 17,
    type: Wrinkle.V1.RegisterPushTokenResponse,
    json_name: "registerPushToken",
    oneof: 0

  field :register, 18, type: Wrinkle.V1.RegisterResponse, oneof: 0
  field :sign_in, 19, type: Wrinkle.V1.SignInResponse, json_name: "signIn", oneof: 0

  field :change_password, 20,
    type: Wrinkle.V1.ChangePasswordResponse,
    json_name: "changePassword",
    oneof: 0

  field :update_profile, 21,
    type: Wrinkle.V1.UpdateProfileResponse,
    json_name: "updateProfile",
    oneof: 0

  field :sign_out, 22, type: Wrinkle.V1.SignOutResponse, json_name: "signOut", oneof: 0

  field :list_devices, 23,
    type: Wrinkle.V1.ListDevicesResponse,
    json_name: "listDevices",
    oneof: 0

  field :rename_device, 24,
    type: Wrinkle.V1.RenameDeviceResponse,
    json_name: "renameDevice",
    oneof: 0

  field :sign_out_device, 25,
    type: Wrinkle.V1.SignOutDeviceResponse,
    json_name: "signOutDevice",
    oneof: 0

  field :delete_account, 26,
    type: Wrinkle.V1.DeleteAccountResponse,
    json_name: "deleteAccount",
    oneof: 0

  field :create_upload, 27,
    type: Wrinkle.V1.CreateUploadResponse,
    json_name: "createUpload",
    oneof: 0

  field :create_group, 28,
    type: Wrinkle.V1.CreateGroupResponse,
    json_name: "createGroup",
    oneof: 0

  field :add_members, 29, type: Wrinkle.V1.AddMembersResponse, json_name: "addMembers", oneof: 0

  field :remove_member, 30,
    type: Wrinkle.V1.RemoveMemberResponse,
    json_name: "removeMember",
    oneof: 0

  field :leave_group, 31, type: Wrinkle.V1.LeaveGroupResponse, json_name: "leaveGroup", oneof: 0
  field :set_topic, 32, type: Wrinkle.V1.SetTopicResponse, json_name: "setTopic", oneof: 0
  field :set_admin, 33, type: Wrinkle.V1.SetAdminResponse, json_name: "setAdmin", oneof: 0
  field :get_members, 34, type: Wrinkle.V1.GetMembersResponse, json_name: "getMembers", oneof: 0
end

defmodule Wrinkle.V1.Error do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Error",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :code, 1, type: Wrinkle.V1.ErrorCode, enum: true
  field :message, 2, type: :string
end

defmodule Wrinkle.V1.Ack do
  @moduledoc false

  use Protobuf, full_name: "wrinkle.v1.Ack", protoc_gen_elixir_version: "0.17.1", syntax: :proto3

  field :cursor, 1, type: :uint64
end

defmodule Wrinkle.V1.Ping do
  @moduledoc false

  use Protobuf, full_name: "wrinkle.v1.Ping", protoc_gen_elixir_version: "0.17.1", syntax: :proto3
end

defmodule Wrinkle.V1.Pong do
  @moduledoc false

  use Protobuf, full_name: "wrinkle.v1.Pong", protoc_gen_elixir_version: "0.17.1", syntax: :proto3
end
