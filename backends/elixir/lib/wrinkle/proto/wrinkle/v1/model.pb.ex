defmodule Wrinkle.V1.User do
  @moduledoc false

  use Protobuf, full_name: "wrinkle.v1.User", protoc_gen_elixir_version: "0.17.1", syntax: :proto3

  field :user_id, 1, type: :bytes, json_name: "userId"
  field :name, 2, type: :string
  field :icon, 3, type: :string
  field :avatar, 4, type: :bytes
  field :deleted, 5, type: :bool
end

defmodule Wrinkle.V1.Device do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Device",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :device_id, 1, type: :uint64, json_name: "deviceId"
  field :name, 2, type: :string
  field :created_at, 3, type: Google.Protobuf.Timestamp, json_name: "createdAt"
  field :last_seen_at, 4, type: Google.Protobuf.Timestamp, json_name: "lastSeenAt"
  field :push, 5, type: :bool
end

defmodule Wrinkle.V1.Message do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Message",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :seq, 2, type: :uint64
  field :sender_user_id, 3, type: :bytes, json_name: "senderUserId"
  field :client_message_id, 4, type: :string, json_name: "clientMessageId"
  field :text, 5, type: :string
  field :sent_at, 6, type: Google.Protobuf.Timestamp, json_name: "sentAt"
end

defmodule Wrinkle.V1.Conversation do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Conversation",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :peer, 2, type: Wrinkle.V1.User
  field :last_seq, 3, type: :uint64, json_name: "lastSeq"
  field :last_message, 4, type: Wrinkle.V1.Message, json_name: "lastMessage"
  field :read_seq, 5, type: :uint64, json_name: "readSeq"
  field :peer_read_seq, 6, type: :uint64, json_name: "peerReadSeq"
end
