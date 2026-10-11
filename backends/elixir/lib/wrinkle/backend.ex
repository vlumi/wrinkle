defmodule Wrinkle.Backend do
  @moduledoc """
  The services process over the internal RPC. One HTTP/2 channel, shared by
  every connection; requests are concurrent streams on it. The gateway starts
  without it: this process connects in the background, retrying with backoff,
  and requests are answered as unavailable until it has connected.
  """
  use GenServer
  require Logger

  alias Wrinkle.Backend.V1.{AckRequest, BackendService, CallRequest}

  @timeout 10_000
  @retry_initial 100
  @retry_max 2_000
  # Reconnections of a channel that dropped, by the adapter, with its backoff.
  @reconnects 1_000_000

  def start_link(_), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @impl true
  def init(_) do
    {:ok, %{addr: Application.fetch_env!(:wrinkle, :services), retry: @retry_initial},
     {:continue, :connect}}
  end

  @impl true
  def handle_continue(:connect, state), do: connect(state)

  @impl true
  def handle_info(:connect, state), do: connect(state)
  def handle_info(_other, state), do: {:noreply, state}

  defp connect(state) do
    opts = [adapter: GRPC.Client.Adapters.Mint, adapter_opts: [retry: @reconnects]]

    case GRPC.Stub.connect(state.addr, opts) do
      {:ok, channel} ->
        :persistent_term.put({__MODULE__, :channel}, channel)
        Logger.info("connected to the services at #{state.addr}")
        {:noreply, state}

      {:error, reason} ->
        Logger.warning(
          "services at #{state.addr} unreachable, retrying in #{state.retry} ms: #{inspect(reason)}"
        )

        Process.send_after(self(), :connect, state.retry)
        {:noreply, %{state | retry: min(state.retry * 2, @retry_max)}}
    end
  end

  defp channel, do: :persistent_term.get({__MODULE__, :channel}, nil)

  @doc """
  Handles one request for a device; returns the response as it goes to the
  device, or an error when the services are not connected, fail or take over
  10 seconds.
  """
  def call(user_id, device_id, request) do
    req = %CallRequest{user_id: user_id, device_id: device_id, request: request}

    guarded(fn channel ->
      with {:ok, %{response: response}} <-
             BackendService.Stub.call(channel, req, timeout: @timeout) do
        {:ok, response}
      end
    end)
  end

  def ack(user_id, device_id, cursor) do
    req = %AckRequest{user_id: user_id, device_id: device_id, cursor: cursor}

    guarded(fn channel ->
      with {:ok, _} <- BackendService.Stub.ack(channel, req, timeout: @timeout), do: :ok
    end)
  end

  # A failure of any kind is an error result, so every request is answered.
  defp guarded(fun) do
    case channel() do
      nil -> {:error, :not_connected}
      channel -> fun.(channel)
    end
  catch
    kind, reason -> {:error, {kind, reason}}
  end
end
