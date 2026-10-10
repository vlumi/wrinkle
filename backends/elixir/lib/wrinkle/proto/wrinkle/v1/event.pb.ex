defmodule Wrinkle.V1.Event do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Event",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof(:body, 0)

  field(:cursor, 1, type: :uint64)
  field(:occurred_at, 2, type: Google.Protobuf.Timestamp, json_name: "occurredAt")
  field(:message, 10, type: Wrinkle.V1.MessageEvent, oneof: 0)
  field(:read, 11, type: Wrinkle.V1.ReadEvent, oneof: 0)

  field(:message_failed, 12,
    type: Wrinkle.V1.MessageFailedEvent,
    json_name: "messageFailed",
    oneof: 0
  )
end

defmodule Wrinkle.V1.MessageEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MessageEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field(:message, 1, type: Wrinkle.V1.Message)
end

defmodule Wrinkle.V1.ReadEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.ReadEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field(:conversation_id, 1, type: :uint64, json_name: "conversationId")
  field(:user_id, 2, type: :uint64, json_name: "userId")
  field(:seq, 3, type: :uint64)
end

defmodule Wrinkle.V1.MessageFailedEvent do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.MessageFailedEvent",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field(:conversation_id, 1, type: :uint64, json_name: "conversationId")
  field(:client_message_id, 2, type: :string, json_name: "clientMessageId")
  field(:reason, 3, type: :string)
end
