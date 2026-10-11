defmodule Wrinkle.V1.Content do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Content",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :body, 0

  field :text, 1, type: :string, oneof: 0
  field :sticker, 2, type: Wrinkle.V1.StickerRef, oneof: 0
  field :attachments, 3, type: Wrinkle.V1.Attachments, oneof: 0
end

defmodule Wrinkle.V1.Attachments do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Attachments",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :items, 1, repeated: true, type: Wrinkle.V1.Attachment
  field :caption, 2, type: :string
end

defmodule Wrinkle.V1.Attachment do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Attachment",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :blob_id, 1, type: :bytes, json_name: "blobId"
  field :key, 2, type: :bytes
  field :digest, 3, type: :bytes
  field :media_type, 4, type: :string, json_name: "mediaType"
  field :size, 5, type: :uint64
  field :name, 6, type: :string
  field :width, 7, type: :uint32
  field :height, 8, type: :uint32
  field :thumbnail, 9, type: :bytes
end

defmodule Wrinkle.V1.StickerRef do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.StickerRef",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :sticker_id, 1, type: :bytes, json_name: "stickerId"
end

defmodule Wrinkle.V1.Reactions do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Reactions",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :emoji, 1, repeated: true, type: :string
end
