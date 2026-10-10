defmodule Wrinkle.Backend.V1.CallRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.backend.v1.CallRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user_id, 1, type: :uint64, json_name: "userId"
  field :device_id, 2, type: :uint64, json_name: "deviceId"
  field :request, 3, type: Wrinkle.V1.Request
end

defmodule Wrinkle.Backend.V1.CallResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.backend.v1.CallResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :response, 1, type: Wrinkle.V1.Response
end

defmodule Wrinkle.Backend.V1.AckRequest do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.backend.v1.AckRequest",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3

  field :user_id, 1, type: :uint64, json_name: "userId"
  field :device_id, 2, type: :uint64, json_name: "deviceId"
  field :cursor, 3, type: :uint64
end

defmodule Wrinkle.Backend.V1.AckResponse do
  @moduledoc false

  use Protobuf,
    full_name: "wrinkle.backend.v1.AckResponse",
    protoc_gen_elixir_version: "0.17.1",
    syntax: :proto3
end

defmodule Wrinkle.Backend.V1.BackendService.Service do
  @moduledoc false

  use GRPC.Service, name: "wrinkle.backend.v1.BackendService", protoc_gen_elixir_version: "0.17.1"

  rpc :Call, Wrinkle.Backend.V1.CallRequest, Wrinkle.Backend.V1.CallResponse

  rpc :Ack, Wrinkle.Backend.V1.AckRequest, Wrinkle.Backend.V1.AckResponse
end

defmodule Wrinkle.Backend.V1.BackendService.Stub do
  @moduledoc false

  use GRPC.Stub, service: Wrinkle.Backend.V1.BackendService.Service
end
