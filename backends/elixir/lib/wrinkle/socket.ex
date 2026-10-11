defmodule Wrinkle.Socket do
  @moduledoc """
  One device's WebSocket connection: a process. Frames in are decoded and
  requests forwarded to the services in a task each, so a slow request does
  not block pushes; responses and pushes are written from this process, so
  its mailbox is the outbound queue.
  """
  @behaviour WebSock
  require Logger

  alias Wrinkle.{AckWriter, Backend, Metrics, Presence, Push, TokenBucket}
  alias Wrinkle.V1.{Frame, Pong, Request, Response}
  alias Wrinkle.V1.Error, as: WireError

  @connect_deadline 5_000
  @max_in_flight 32
  @outbound_queue 32
  # Only pushes close a connection that does not keep up. Responses wait in
  # the mailbox too, at most the in-flight cap of them, and pile up there
  # while this process reads a burst of frames.
  @max_mailbox @outbound_queue + @max_in_flight

  @shutting_down {1001, "shutting down"}

  @doc "Sends a message to every open connection on this node."
  def broadcast(message) do
    Registry.dispatch(Wrinkle.Sockets, :socket, fn entries ->
      for {pid, _} <- entries, do: send(pid, message)
    end)
  end

  @doc "The number of open connections on this node."
  def count, do: Registry.count(Wrinkle.Sockets)

  # device is nil until Connect succeeds, then {user_id, device_id}; seen_at
  # is when a frame last arrived, in monotonic milliseconds; ack is the
  # AckWriter state.
  @impl true
  def init(_opts) do
    Metrics.connection(+1)
    {:ok, _} = Registry.register(Wrinkle.Sockets, :socket, nil)
    Process.send_after(self(), :connect_deadline, @connect_deadline)
    now = System.monotonic_time(:millisecond)

    {:ok,
     %{
       device: nil,
       bucket: TokenBucket.new(now),
       in_flight: 0,
       ack: nil,
       seen_at: now,
       draining: false
     }}
  end

  @impl true
  def handle_in({data, [opcode: :binary]}, state) do
    now = System.monotonic_time(:millisecond)
    state = %{state | seen_at: now}

    case decode(data) do
      {:ok, %Frame{kind: {kind, _} = body}} when kind in [:request, :ack, :ping, :pong] ->
        {result, bucket} = TokenBucket.take(state.bucket, now)
        inbound(body, result, %{state | bucket: bucket})

      {:ok, _} ->
        {:stop, :normal, {1008, "unexpected frame"}, state}

      :error ->
        {:stop, :normal, {1008, "malformed frame"}, state}
    end
  end

  def handle_in({_data, [opcode: :text]}, state) do
    {:stop, :normal, {1003, "frames are binary protobuf"}, state}
  end

  @impl true
  def handle_control(_frame, state) do
    {:ok, %{state | seen_at: System.monotonic_time(:millisecond)}}
  end

  @impl true
  def handle_info({:response, {response, connected}}, state) do
    state = attach(%{state | in_flight: state.in_flight - 1}, connected)
    frame = encode({:response, response})

    if state.draining and state.in_flight == 0 do
      {:stop, :normal, @shutting_down, frame, state}
    else
      {:push, frame, state}
    end
  end

  def handle_info({:push, frame}, state) do
    if too_slow?() do
      {:stop, :normal, {1013, "too slow"}, state}
    else
      Metrics.pushed()
      {:push, {:binary, frame}, state}
    end
  end

  def handle_info(:ack_done, state) do
    {action, ack} = AckWriter.done(state.ack)
    write_ack(action, state.device)
    {:ok, %{state | ack: ack}}
  end

  def handle_info(:connect_deadline, %{device: nil} = state) do
    {:stop, :normal, {1008, "connect first"}, state}
  end

  def handle_info({:keepalive, idle_before}, state) do
    if state.seen_at <= idle_before do
      {:stop, :normal, {1001, "idle"}, state}
    else
      {:push, {:ping, ""}, state}
    end
  end

  def handle_info({:drain, _grace}, %{in_flight: 0} = state) do
    {:stop, :normal, @shutting_down, state}
  end

  def handle_info({:drain, grace}, state) do
    Process.send_after(self(), :drain_deadline, grace)
    {:ok, %{state | draining: true}}
  end

  def handle_info(:drain_deadline, state), do: {:stop, :normal, @shutting_down, state}

  def handle_info(_other, state), do: {:ok, state}

  @impl true
  def terminate(_reason, state) do
    Metrics.connection(-1)
    # At once rather than at exit, which waits for the closing handshake.
    Registry.unregister(Wrinkle.Sockets, :socket)

    with {user_id, device_id} <- state.device do
      Registry.unregister(Wrinkle.Connections, user_id)
      Push.detach(user_id)
      Presence.delete(user_id, device_id)
    end

    :ok
  end

  defp decode(data) do
    {:ok, Frame.decode(data)}
  rescue
    _ -> :error
  end

  defp inbound({:request, request}, :empty, state), do: reject(request, "rate limited", state)
  defp inbound(_body, :empty, state), do: {:ok, state}

  defp inbound({:request, %Request{id: id}}, :ok, %{draining: true} = state) do
    {:push, encode({:response, error(id, :ERROR_CODE_UNAVAILABLE, "shutting down")}), state}
  end

  defp inbound({:request, request}, :ok, %{in_flight: n} = state) when n >= @max_in_flight do
    reject(request, "too many requests in flight", state)
  end

  defp inbound({:request, request}, :ok, state) do
    socket = self()
    device = state.device
    Task.start(fn -> send(socket, {:response, handle_request(device, request)}) end)
    {:ok, %{state | in_flight: state.in_flight + 1}}
  end

  defp inbound({:ack, ack}, :ok, %{device: {_, _}} = state) do
    {action, acks} = AckWriter.ack(state.ack, ack.cursor)
    write_ack(action, state.device)
    {:ok, %{state | ack: acks}}
  end

  defp inbound({:ack, _}, :ok, state), do: {:ok, state}
  defp inbound({:ping, _}, :ok, state), do: {:push, encode({:pong, %Pong{}}), state}
  defp inbound({:pong, _}, :ok, state), do: {:ok, state}

  defp reject(%Request{id: id} = request, message, state) do
    response = error(id, :ERROR_CODE_RESOURCE_EXHAUSTED, message)
    Metrics.request(request_type(request), response_code(response), 0)
    {:push, encode({:response, response}), state}
  end

  defp write_ack(:none, _device), do: :ok

  defp write_ack({:write, cursor}, {user_id, device_id}) do
    socket = self()

    Task.start(fn ->
      Backend.ack(user_id, device_id, cursor)
      send(socket, :ack_done)
    end)
  end

  defp too_slow? do
    {:message_queue_len, n} = Process.info(self(), :message_queue_len)
    n > @max_mailbox
  end

  defp encode(kind), do: {:binary, Frame.encode(%Frame{kind: kind})}

  # A connection binds to the first device that connects on it.
  defp attach(state, nil), do: state
  defp attach(%{device: {_, _}} = state, _connected), do: state

  defp attach(state, {user_id, device_id} = device) do
    {:ok, _} = Registry.register(Wrinkle.Connections, user_id, device_id)
    Push.attach(user_id)
    Presence.put(user_id, device_id)
    %{state | device: device}
  end

  # Returns the response and, after a successful Connect, the device to attach.
  defp handle_request(device, %Request{} = request) do
    started = System.monotonic_time()
    {response, connected} = dispatch(device, request)

    Metrics.request(
      request_type(request),
      response_code(response),
      System.monotonic_time() - started
    )

    {response, connected}
  end

  defp dispatch(device, %Request{id: id} = request) do
    case {request.body, device} do
      {{:connect, _}, {_, _}} ->
        {error(id, :ERROR_CODE_FAILED_PRECONDITION, "already connected"), nil}

      {{:connect, _}, nil} ->
        forward({0, 0}, request)

      {_, nil} ->
        {error(id, :ERROR_CODE_UNAUTHENTICATED, "connect first"), nil}

      {_, device} ->
        forward(device, request)
    end
  end

  defp forward({user_id, device_id}, %Request{id: id} = request) do
    case Backend.call(user_id, device_id, request) do
      {:ok, %Response{body: {:connect, c}} = response} ->
        {response, {c.user.user_id, c.device_id}}

      {:ok, response} ->
        {response, nil}

      {:error, reason} ->
        Logger.error("backend call failed: #{inspect(reason)}")
        {error(id, :ERROR_CODE_UNAVAILABLE, "services unavailable"), nil}
    end
  end

  defp error(id, code, message) do
    %Response{request_id: id, body: {:error, %WireError{code: code, message: message}}}
  end

  defp request_type(%Request{body: {type, _}}), do: Atom.to_string(type)
  defp request_type(_), do: "unknown"

  defp response_code(%Response{body: {:error, %WireError{code: code}}}), do: Atom.to_string(code)
  defp response_code(_), do: "ok"
end
