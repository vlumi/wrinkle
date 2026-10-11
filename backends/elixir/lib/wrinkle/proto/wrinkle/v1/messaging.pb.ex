defmodule Wrinkle.V1.PushService do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "wrinkle.v1.PushService",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :PUSH_SERVICE_UNSPECIFIED, 0
  field :PUSH_SERVICE_APNS_SANDBOX, 1
  field :PUSH_SERVICE_APNS, 2
end

defmodule Wrinkle.V1.RegisterRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.RegisterRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :name, 1, type: :string
  field :password, 2, type: :string
  field :device_name, 3, type: :string, json_name: "deviceName"
end

defmodule Wrinkle.V1.RegisterResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.RegisterResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :credentials, 1, type: Wrinkle.V1.DeviceCredentials
end

defmodule Wrinkle.V1.SignInRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SignInRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :name, 1, type: :string
  field :password, 2, type: :string
  field :device_name, 3, type: :string, json_name: "deviceName"
end

defmodule Wrinkle.V1.SignInResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SignInResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :credentials, 1, type: Wrinkle.V1.DeviceCredentials
end

defmodule Wrinkle.V1.DeviceCredentials do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.DeviceCredentials",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user_id, 1, type: :bytes, json_name: "userId"
  field :device_id, 2, type: :uint64, json_name: "deviceId"
  field :device_token, 3, type: :string, json_name: "deviceToken"
end

defmodule Wrinkle.V1.ConnectRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ConnectRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :device_token, 1, type: :string, json_name: "deviceToken"
end

defmodule Wrinkle.V1.ConnectResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ConnectResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user, 1, type: Wrinkle.V1.User
  field :device_id, 2, type: :uint64, json_name: "deviceId"
  field :device_cursor, 3, type: :uint64, json_name: "deviceCursor"
  field :log_head, 4, type: :uint64, json_name: "logHead"
end

defmodule Wrinkle.V1.ChangePasswordRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ChangePasswordRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :current_password, 1, type: :string, json_name: "currentPassword"
  field :new_password, 2, type: :string, json_name: "newPassword"
end

defmodule Wrinkle.V1.ChangePasswordResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ChangePasswordResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3
end

defmodule Wrinkle.V1.UpdateProfileRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.UpdateProfileRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :name, 1, proto3_optional: true, type: :string
  field :icon, 2, proto3_optional: true, type: :string
end

defmodule Wrinkle.V1.UpdateProfileResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.UpdateProfileResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user, 1, type: Wrinkle.V1.User
end

defmodule Wrinkle.V1.SignOutRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SignOutRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3
end

defmodule Wrinkle.V1.SignOutResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SignOutResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3
end

defmodule Wrinkle.V1.OpenConversationRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.OpenConversationRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :peer_name, 1, type: :string, json_name: "peerName"
end

defmodule Wrinkle.V1.OpenConversationResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.OpenConversationResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation, 1, type: Wrinkle.V1.Conversation
end

defmodule Wrinkle.V1.SendMessageRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SendMessageRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :client_message_id, 2, type: :string, json_name: "clientMessageId"
  field :text, 3, type: :string
end

defmodule Wrinkle.V1.SendMessageResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SendMessageResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :message, 1, type: Wrinkle.V1.Message
end

defmodule Wrinkle.V1.MarkReadRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MarkReadRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :seq, 2, type: :uint64
end

defmodule Wrinkle.V1.MarkReadResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MarkReadResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3
end

defmodule Wrinkle.V1.SyncRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SyncRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :cursor, 1, type: :uint64
  field :limit, 2, type: :uint32
end

defmodule Wrinkle.V1.SyncResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.SyncResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :events, 1, repeated: true, type: Wrinkle.V1.Event
  field :cursor, 2, type: :uint64
  field :has_more, 3, type: :bool, json_name: "hasMore"
end

defmodule Wrinkle.V1.ListConversationsRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ListConversationsRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3
end

defmodule Wrinkle.V1.ListConversationsResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ListConversationsResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversations, 1, repeated: true, type: Wrinkle.V1.Conversation
end

defmodule Wrinkle.V1.GetMessagesRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.GetMessagesRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :before_seq, 2, type: :uint64, json_name: "beforeSeq"
  field :limit, 3, type: :uint32
end

defmodule Wrinkle.V1.GetMessagesResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.GetMessagesResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :messages, 1, repeated: true, type: Wrinkle.V1.Message
end

defmodule Wrinkle.V1.RegisterPushTokenRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.RegisterPushTokenRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :service, 1, type: Wrinkle.V1.PushService, enum: true
  field :token, 2, type: :string
end

defmodule Wrinkle.V1.RegisterPushTokenResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.RegisterPushTokenResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3
end
