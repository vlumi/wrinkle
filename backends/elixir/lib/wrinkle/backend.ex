defmodule Wrinkle.Backend do
  @moduledoc """
  The services process over the internal RPC. One HTTP/2 channel, shared by
  every connection; requests are concurrent streams on it.
  """
  use GenServer

  alias Wrinkle.Backend.V1.{AckRequest, BackendService, CallRequest}

  @timeout 10_000

  def start_link(_), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @impl true
  def init(_) do
    addr = Application.fetch_env!(:wrinkle, :services)

    case GRPC.Stub.connect(addr, adapter: GRPC.Client.Adapters.Mint) do
      {:ok, channel} ->
        :persistent_term.put({__MODULE__, :channel}, channel)
        {:ok, channel}

      {:error, reason} ->
        {:stop, {:services_unreachable, addr, reason}}
    end
  end

  defp channel, do: :persistent_term.get({__MODULE__, :channel})

  @doc "Handles one request for a device; returns the response as it goes to the device."
  def call(user_id, device_id, request) do
    req = %CallRequest{user_id: user_id, device_id: device_id, request: request}

    case BackendService.Stub.call(channel(), req, timeout: @timeout) do
      {:ok, %{response: response}} -> {:ok, response}
      {:error, reason} -> {:error, reason}
    end
  end

  def ack(user_id, device_id, cursor) do
    req = %AckRequest{user_id: user_id, device_id: device_id, cursor: cursor}

    case BackendService.Stub.ack(channel(), req, timeout: @timeout) do
      {:ok, _} -> :ok
      {:error, reason} -> {:error, reason}
    end
  end
end
