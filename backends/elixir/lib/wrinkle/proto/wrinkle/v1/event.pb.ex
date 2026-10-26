defmodule Wrinkle.V1.Event do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Event",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :body, 0

  field :cursor, 1, type: :uint64
  field :occurred_at, 2, type: Google.Protobuf.Timestamp, json_name: "occurredAt"
  field :message, 10, type: Wrinkle.V1.MessageEvent, oneof: 0
  field :read, 11, type: Wrinkle.V1.ReadEvent, oneof: 0
  field :profile, 13, type: Wrinkle.V1.ProfileEvent, oneof: 0
  field :device, 14, type: Wrinkle.V1.DeviceEvent, oneof: 0

  field :message_deleted, 15,
    type: Wrinkle.V1.MessageDeletedEvent,
    json_name: "messageDeleted",
    oneof: 0

  field :reaction, 16, type: Wrinkle.V1.ReactionEvent, oneof: 0
  field :palette, 17, type: Wrinkle.V1.PaletteEvent, oneof: 0
  field :contact, 18, type: Wrinkle.V1.ContactEvent, oneof: 0
end

defmodule Wrinkle.V1.MessageEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MessageEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :message, 1, type: Wrinkle.V1.Message
end

defmodule Wrinkle.V1.ReadEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ReadEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :user_id, 2, type: :bytes, json_name: "userId"
  field :seq, 3, type: :uint64
end

defmodule Wrinkle.V1.ProfileEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ProfileEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user, 1, type: Wrinkle.V1.User
end

defmodule Wrinkle.V1.DeviceEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.DeviceEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :device, 1, type: Wrinkle.V1.Device
  field :signed_out, 2, type: :bool, json_name: "signedOut"
  field :user_id, 3, type: :bytes, json_name: "userId"
end

defmodule Wrinkle.V1.MessageDeletedEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MessageDeletedEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :seq, 2, type: :uint64
  field :deleted_at, 3, type: Google.Protobuf.Timestamp, json_name: "deletedAt"
end

defmodule Wrinkle.V1.ReactionEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ReactionEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :conversation_id, 1, type: :bytes, json_name: "conversationId"
  field :seq, 2, type: :uint64
  field :user_id, 3, type: :bytes, json_name: "userId"
  field :sealed, 4, type: Wrinkle.V1.Sealed
  field :updated_at, 5, type: Google.Protobuf.Timestamp, json_name: "updatedAt"
end

defmodule Wrinkle.V1.PaletteEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.PaletteEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :sticker_ids, 1, repeated: true, type: :bytes, json_name: "stickerIds"
end

defmodule Wrinkle.V1.ContactEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ContactEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :contact, 1, type: Wrinkle.V1.Contact
end
