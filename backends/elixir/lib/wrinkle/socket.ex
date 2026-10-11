defmodule Wrinkle.Socket do
  @moduledoc """
  One device's WebSocket connection: a process. Frames in are decoded and
  requests forwarded to the services in a supervised task each, so a slow
  request does not block pushes; responses and pushes are written from this
  process, so its mailbox is the outbound queue.
  """
  @behaviour WebSock
  require Logger

  alias Wrinkle.{AckWriter, Backend, Metrics, Presence, Push, TokenBucket}
  alias Wrinkle.V1.{Frame, Pong, Request, Response}
  alias Wrinkle.V1.Error, as: WireError

  @connect_deadline 5_000
  @subscribe_timeout 5_000
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

  # device is nil until Connect succeeds, then {user_id, device_id}; connect
  # is :none, :pending while a Connect is forwarded, {:subscribing, ...} while
  # its response waits for the push subscription, then :done; tasks maps each
  # task's monitor to {:request, id, type, started} or :ack; seen_at is when a
  # frame last arrived, in monotonic milliseconds; ack is the AckWriter state;
  # push monitors Wrinkle.Push once attached.
  @impl true
  def init(_opts) do
    Metrics.connection(+1)
    {:ok, _} = Registry.register(Wrinkle.Sockets, :socket, nil)
    Process.send_after(self(), :connect_deadline, @connect_deadline)
    now = System.monotonic_time(:millisecond)

    {:ok,
     %{
       device: nil,
       connect: :none,
       tasks: %{},
       push: nil,
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
  def handle_info({ref, result}, %{tasks: tasks} = state) when is_map_key(tasks, ref) do
    Process.demonitor(ref, [:flush])
    {task, tasks} = Map.pop(tasks, ref)
    finished(task, result, %{state | tasks: tasks})
  end

  def handle_info({:DOWN, ref, :process, _pid, _reason}, %{tasks: tasks} = state)
      when is_map_key(tasks, ref) do
    {task, tasks} = Map.pop(tasks, ref)
    crashed(task, %{state | tasks: tasks})
  end

  def handle_info({:DOWN, ref, :process, _pid, _reason}, %{push: ref} = state) do
    {:stop, :normal, {1011, "push subscriptions lost"}, state}
  end

  def handle_info(
        {:subscribed, _user_id},
        %{connect: {:subscribing, id, response, started}} = state
      ) do
    respond({id, "connect", started}, response, %{state | connect: :done})
  end

  # Not connected after all: Connect may be retried, within a fresh deadline.
  def handle_info(
        :subscribe_deadline,
        %{connect: {:subscribing, id, _, started}, device: {user_id, device_id}} = state
      ) do
    Logger.warning("push subscription of user #{user_id} not ready, connect refused")
    Push.detach()
    Presence.delete(user_id, device_id)
    Process.demonitor(state.push, [:flush])
    Process.send_after(self(), :connect_deadline, @connect_deadline)
    response = error(id, :ERROR_CODE_UNAVAILABLE, "push channel unavailable")
    respond({id, "connect", started}, response, %{state | device: nil, connect: :none, push: nil})
  end

  def handle_info({:push, frame}, state) do
    if too_slow?() do
      {:stop, :normal, {1013, "too slow"}, state}
    else
      Metrics.pushed()
      {:push, {:binary, frame}, state}
    end
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
      Push.detach()
      Presence.delete(user_id, device_id)
    end

    :ok
  end

  defp decode(data) do
    {:ok, Frame.decode(data)}
  rescue
    _ -> :error
  end

  defp inbound({:request, request}, :empty, state) do
    refuse(request, :ERROR_CODE_RESOURCE_EXHAUSTED, "rate limited", state)
  end

  defp inbound(_body, :empty, state), do: {:ok, state}

  defp inbound({:request, request}, :ok, %{draining: true} = state) do
    refuse(request, :ERROR_CODE_UNAVAILABLE, "shutting down", state)
  end

  defp inbound({:request, request}, :ok, %{in_flight: n} = state) when n >= @max_in_flight do
    refuse(request, :ERROR_CODE_RESOURCE_EXHAUSTED, "too many requests in flight", state)
  end

  # One Connect per connection, also while the first is still forwarded.
  defp inbound({:request, %Request{body: {:connect, _}} = request}, :ok, %{connect: c} = state)
       when c != :none do
    refuse(request, :ERROR_CODE_FAILED_PRECONDITION, "already connected", state)
  end

  defp inbound({:request, %Request{id: id} = request}, :ok, state) do
    device = state.device
    task = Task.Supervisor.async_nolink(Wrinkle.Tasks, fn -> dispatch(device, request) end)
    type = request_type(request)
    entry = {:request, id, type, System.monotonic_time()}
    connect = if type == "connect", do: :pending, else: state.connect

    {:ok,
     %{
       state
       | tasks: Map.put(state.tasks, task.ref, entry),
         in_flight: state.in_flight + 1,
         connect: connect
     }}
  end

  defp inbound({:ack, ack}, :ok, %{device: {_, _}} = state) do
    {action, acks} = AckWriter.ack(state.ack, ack.cursor)
    {:ok, write_ack(action, %{state | ack: acks})}
  end

  defp inbound({:ack, _}, :ok, state), do: {:ok, state}
  defp inbound({:ping, _}, :ok, state), do: {:push, encode({:pong, %Pong{}}), state}
  defp inbound({:pong, _}, :ok, state), do: {:ok, state}

  # Answered at once, without the services; counted with a duration of zero.
  defp refuse(%Request{id: id} = request, code, message, state) do
    response = error(id, code, message)
    Metrics.request(request_type(request), response_code(response), 0)
    {:push, encode({:response, response}), state}
  end

  # A successful Connect binds the connection to the device; its response
  # waits until the user's push channel is subscribed.
  defp finished({:request, id, _type, started}, {response, {user_id, device_id} = device}, state) do
    Push.attach(user_id)
    Presence.put(user_id, device_id)
    Process.send_after(self(), :subscribe_deadline, @subscribe_timeout)

    {:ok,
     %{
       state
       | device: device,
         connect: {:subscribing, id, response, started},
         push: Process.monitor(Push)
     }}
  end

  defp finished({:request, id, type, started}, {response, nil}, state) do
    respond({id, type, started}, response, connect_failed(type, state))
  end

  defp finished(:ack, _result, state), do: ack_done(state)

  # A task that crashed: its request is answered, an ack write is done.
  defp crashed({:request, id, type, started}, state) do
    response = error(id, :ERROR_CODE_INTERNAL, "internal error")
    respond({id, type, started}, response, connect_failed(type, state))
  end

  defp crashed(:ack, state), do: ack_done(state)

  defp connect_failed("connect", state), do: %{state | connect: :none}
  defp connect_failed(_type, state), do: state

  defp respond({_id, type, started}, response, state) do
    Metrics.request(type, response_code(response), System.monotonic_time() - started)
    state = %{state | in_flight: state.in_flight - 1}
    frame = encode({:response, response})

    if state.draining and state.in_flight == 0 do
      {:stop, :normal, @shutting_down, frame, state}
    else
      {:push, frame, state}
    end
  end

  defp ack_done(state) do
    {action, ack} = AckWriter.done(state.ack)
    {:ok, write_ack(action, %{state | ack: ack})}
  end

  defp write_ack(:none, state), do: state

  defp write_ack({:write, cursor}, %{device: {user_id, device_id}} = state) do
    task =
      Task.Supervisor.async_nolink(Wrinkle.Tasks, fn ->
        Backend.ack(user_id, device_id, cursor)
      end)

    %{state | tasks: Map.put(state.tasks, task.ref, :ack)}
  end

  defp too_slow? do
    {:message_queue_len, n} = Process.info(self(), :message_queue_len)
    n > @max_mailbox
  end

  defp encode(kind), do: {:binary, Frame.encode(%Frame{kind: kind})}

  # Runs in the task: the response and, after a successful Connect, the
  # device to bind to.
  defp dispatch(device, %Request{id: id} = request) do
    case {request.body, device} do
      {{:connect, _}, _} -> forward({0, 0}, request)
      {_, nil} -> {error(id, :ERROR_CODE_UNAUTHENTICATED, "connect first"), nil}
      {_, device} -> forward(device, request)
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
