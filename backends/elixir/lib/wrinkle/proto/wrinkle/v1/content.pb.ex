defmodule Wrinkle.V1.Content do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Content",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  oneof :body, 0

  field :text, 1, type: :string, oneof: 0
end

defmodule Wrinkle.V1.Reactions do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.v1.Reactions",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :emoji, 1, repeated: true, type: :string
end
