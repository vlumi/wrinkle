defmodule Wrinkle.V1.ContactState do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "wrinkle.v1.ContactState",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :CONTACT_STATE_UNSPECIFIED, 0
  field :CONTACT_STATE_CONTACT, 1
  field :CONTACT_STATE_INCOMING, 2
  field :CONTACT_STATE_OUTGOING, 3
  field :CONTACT_STATE_BLOCKED, 4
end

defmodule Wrinkle.V1.ConversationKind do
  @moduledoc false

  use Protobuf,
    enum: true,
    full_name: "wrinkle.v1.ConversationKind",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :CONVERSATION_KIND_UNSPECIFIED, 0
  field :CONVERSATION_KIND_DIRECT, 1
  field :CONVERSATION_KIND_GROUP, 2
end

defmodule Wrinkle.V1.User do
  @moduledoc false

  use Protobuf, full_name: "wrinkle.v1.User", protoc_gen_elixir_version: "0.17.1", syntax: :proto3

  field :user_id, 1, type: :bytes, json_name: "userId"
  field :name, 2, type: :string
  field :icon, 3, type: :string
  field :avatar, 4, type: :bytes
  field :deleted, 5, type: :bool
end

defmodule Wrinkle.V1.Sticker do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Sticker",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :sticker_id, 1, type: :bytes, json_name: "stickerId"
  field :creator_user_id, 2, type: :bytes, json_name: "creatorUserId"
  field :media_type, 3, type: :string, json_name: "mediaType"
  field :created_at, 4, type: Google.Protobuf.Timestamp, json_name: "createdAt"
end

defmodule Wrinkle.V1.Contact do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Contact",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user, 1, type: Wrinkle.V1.User
  field :state, 2, type: Wrinkle.V1.ContactState, enum: true
  field :updated_at, 3, type: Google.Protobuf.Timestamp, json_name: "updatedAt"
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
  field :public_key, 6, type: :bytes, json_name: "publicKey"
end

defmodule Wrinkle.V1.Message do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Message",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :content, 0

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :seq, 2, type: :uint64
  field :sender_user_id, 3, type: :bytes, json_name: "senderUserId"
  field :client_message_id, 4, type: :string, json_name: "clientMessageId"
  field :sent_at, 6, type: Google.Protobuf.Timestamp, json_name: "sentAt"
  field :sealed, 9, type: Wrinkle.V1.Sealed, oneof: 0
  field :notice, 7, type: Wrinkle.V1.Notice, oneof: 0
  field :deleted_at, 8, type: Google.Protobuf.Timestamp, json_name: "deletedAt"
  field :blob_ids, 11, repeated: true, type: :bytes, json_name: "blobIds"
  field :reactions, 10, repeated: true, type: Wrinkle.V1.Reaction
end

defmodule Wrinkle.V1.Reaction do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Reaction",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user_id, 1, type: :bytes, json_name: "userId"
  field :sealed, 2, type: Wrinkle.V1.Sealed
  field :updated_at, 3, type: Google.Protobuf.Timestamp, json_name: "updatedAt"
end

defmodule Wrinkle.V1.Sealed do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Sealed",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :epoch, 1, type: :uint32
  field :nonce, 2, type: :bytes
  field :ciphertext, 3, type: :bytes
end

defmodule Wrinkle.V1.Notice do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Notice",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :kind, 0

  field :group_created, 1, type: Wrinkle.V1.GroupCreated, json_name: "groupCreated", oneof: 0
  field :members_added, 2, type: Wrinkle.V1.MembersAdded, json_name: "membersAdded", oneof: 0
  field :member_removed, 3, type: Wrinkle.V1.MemberRemoved, json_name: "memberRemoved", oneof: 0
  field :member_left, 4, type: Wrinkle.V1.MemberLeft, json_name: "memberLeft", oneof: 0
  field :topic_changed, 5, type: Wrinkle.V1.TopicChanged, json_name: "topicChanged", oneof: 0
  field :admin_changed, 6, type: Wrinkle.V1.AdminChanged, json_name: "adminChanged", oneof: 0
end

defmodule Wrinkle.V1.GroupCreated do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.GroupCreated",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :topic, 1, type: :string
  field :members, 2, repeated: true, type: Wrinkle.V1.User
end

defmodule Wrinkle.V1.MembersAdded do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MembersAdded",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :users, 1, repeated: true, type: Wrinkle.V1.User
end

defmodule Wrinkle.V1.MemberRemoved do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MemberRemoved",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user, 1, type: Wrinkle.V1.User
end

defmodule Wrinkle.V1.MemberLeft do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MemberLeft",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :promoted, 1, type: Wrinkle.V1.User
end

defmodule Wrinkle.V1.TopicChanged do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.TopicChanged",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :topic, 1, type: :string
end

defmodule Wrinkle.V1.AdminChanged do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.AdminChanged",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user, 1, type: Wrinkle.V1.User
  field :admin, 2, type: :bool
end

defmodule Wrinkle.V1.Conversation do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Conversation",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :kind, 7, type: Wrinkle.V1.ConversationKind, enum: true
  field :peer, 2, type: Wrinkle.V1.User
  field :last_seq, 3, type: :uint64, json_name: "lastSeq"
  field :last_message, 4, type: Wrinkle.V1.Message, json_name: "lastMessage"
  field :read_seq, 5, type: :uint64, json_name: "readSeq"
  field :peer_read_seq, 6, type: :uint64, json_name: "peerReadSeq"
  field :topic, 8, type: :string
  field :member_count, 9, type: :uint32, json_name: "memberCount"
  field :member, 10, type: :bool
  field :admin, 11, type: :bool
end

defmodule Wrinkle.V1.Member do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Member",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user, 1, type: Wrinkle.V1.User
  field :admin, 2, type: :bool
  field :read_seq, 3, type: :uint64, json_name: "readSeq"
  field :joined_seq, 4, type: :uint64, json_name: "joinedSeq"
end
