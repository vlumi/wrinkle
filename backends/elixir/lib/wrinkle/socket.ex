defmodule Wrinkle.Socket do
  @moduledoc """
  One device's WebSocket connection: a process. Frames in are decoded and
  requests forwarded to the services in a task each, so a slow request does
  not block pushes; responses and pushes are written from this process.
  """
  @behaviour WebSock
  import Bitwise
  require Logger

  alias Wrinkle.V1.{Frame, Pong, Request, Response}
  alias Wrinkle.V1.Error, as: WireError

  @impl true
  def init(_opts) do
    Wrinkle.Metrics.connection(+1)
    {:ok, %{user_id: 0, device_id: 0, connected: false}}
  end

  @impl true
  def handle_in({data, [opcode: :binary]}, state) do
    case Frame.decode(data) do
      %Frame{kind: {:request, request}} ->
        parent = self()
        snapshot = state
        Task.start(fn -> send(parent, {:response, handle_request(snapshot, request)}) end)
        {:ok, state}

      %Frame{kind: {:ack, ack}} ->
        if state.connected do
          %{user_id: u, device_id: d} = state
          Task.start(fn -> Wrinkle.Backend.ack(u, d, ack.cursor) end)
        end

        {:ok, state}

      %Frame{kind: {:ping, _}} ->
        {:push, {:binary, Frame.encode(%Frame{kind: {:pong, %Pong{}}})}, state}

      %Frame{kind: {:pong, _}} ->
        {:ok, state}

      _ ->
        {:stop, :normal, {1008, "unexpected frame"}, state}
    end
  rescue
    Protobuf.DecodeError -> {:stop, :normal, {1008, "malformed frame"}, state}
  end

  def handle_in({_data, [opcode: :text]}, state) do
    {:stop, :normal, {1003, "frames are binary protobuf"}, state}
  end

  @impl true
  def handle_info({:response, {response, connected}}, state) do
    state =
      case connected do
        nil ->
          state

        %{user_id: user_id, device_id: device_id} ->
          if state.connected do
            state
          else
            {:ok, _} = Registry.register(Wrinkle.Connections, user_id, device_id)
            Wrinkle.Push.attach(user_id, device_id)
            %{state | connected: true, user_id: user_id, device_id: device_id}
          end
      end

    {:push, {:binary, Frame.encode(%Frame{kind: {:response, response}})}, state}
  end

  def handle_info({:push, frame}, state) do
    Wrinkle.Metrics.pushed()
    {:push, {:binary, frame}, state}
  end

  def handle_info(_other, state), do: {:ok, state}

  @impl true
  def terminate(_reason, state) do
    Wrinkle.Metrics.connection(-1)
    if state.connected, do: Wrinkle.Push.detach(state.user_id, state.device_id)
    :ok
  end

  # Applies the connection's state rules, then forwards. Returns the response
  # and, after a successful Connect, the identity to attach.
  defp handle_request(state, %Request{id: id} = request) do
    started = System.monotonic_time()
    connect? = match?({:connect, _}, request.body)

    {response, connected} =
      cond do
        connect? and state.connected ->
          {error(id, :ERROR_CODE_FAILED_PRECONDITION, "already connected"), nil}

        not connect? and not state.connected ->
          {error(id, :ERROR_CODE_UNAUTHENTICATED, "connect first"), nil}

        true ->
          case Wrinkle.Backend.call(state.user_id, state.device_id, request) do
            {:ok, %Response{body: {:connect, c}} = response} ->
              {response, %{user_id: c.user.user_id, device_id: c.device_id}}

            {:ok, response} ->
              {response, nil}

            {:error, reason} ->
              Logger.error("backend call failed: #{inspect(reason)}")
              {error(id, :ERROR_CODE_UNAVAILABLE, "services unavailable"), nil}
          end
      end

    code =
      case response.body do
        {:error, %WireError{code: c}} -> Atom.to_string(c)
        _ -> "ok"
      end

    Wrinkle.Metrics.request(request_type(request), code, System.monotonic_time() - started)
    {response, connected}
  end

  defp error(id, code, message) do
    %Response{request_id: id, body: {:error, %WireError{code: code, message: message}}}
  end

  defp request_type(%Request{body: {type, _}}), do: Atom.to_string(type)
  defp request_type(_), do: "unknown"

  @doc "Wraps an already serialized Event in a Frame without decoding it: Frame.event is field 3."
  def event_frame(payload) do
    <<0x1A, varint(byte_size(payload))::binary, payload::binary>>
  end

  defp varint(n) when n < 128, do: <<n>>
  defp varint(n), do: <<1::1, n &&& 0x7F::7, varint(n >>> 7)::binary>>
end
