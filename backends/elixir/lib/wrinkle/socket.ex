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
  alias Wrinkle.V1.Error, as: WireError
  alias Wrinkle.V1.{Frame, Pong, Request, Response}

  @connect_deadline 5_000
  @subscribe_timeout 5_000
  @max_in_flight 32
  # Register and SignIn requests per connection, each of which may cost a
  # password hash; those answered INVALID_ARGUMENT, before hashing, do not count.
  @max_sign_ins 5
  @outbound_queue 32

  @shutting_down {1001, "shutting down"}
  @signed_out {4001, "signed out"}

  @doc "Sends a message to every open connection on this node."
  def broadcast(message) do
    Registry.dispatch(Wrinkle.Sockets, :socket, fn entries ->
      for {pid, _} <- entries, do: send(pid, message)
    end)
  end

  @doc "The number of open connections on this node."
  def count, do: Registry.count(Wrinkle.Sockets)

  # The connect and subscribe deadlines are messages to self, each with a ref;
  # only the latest counts, so a restarted deadline makes the earlier message
  # stale. pushes counts those in the mailbox: up by Wrinkle.Push as it sends
  # one, down here as it is written.
  defstruct device: nil,
            connect: :none,
            deadline: nil,
            tasks: %{},
            push: nil,
            pushes: nil,
            bucket: nil,
            in_flight: 0,
            sign_ins: 0,
            ack: nil,
            seen_at: 0,
            draining: false

  @type device :: {user_id :: <<_::128>>, device_id :: non_neg_integer()}
  @type task ::
          {:request, id :: non_neg_integer(), type :: String.t(), started :: integer()}
          | {:ack, pid()}
  @type connect ::
          :none
          | :pending
          | {:subscribing, id :: non_neg_integer(), Response.t(), started :: integer(),
             reference()}
          | :done
  @type t :: %__MODULE__{
          device: device() | nil,
          connect: connect(),
          deadline: reference() | nil,
          tasks: %{reference() => task()},
          push: reference() | nil,
          pushes: :atomics.atomics_ref(),
          bucket: TokenBucket.t(),
          in_flight: non_neg_integer(),
          sign_ins: non_neg_integer(),
          ack: AckWriter.t(),
          seen_at: integer(),
          draining: boolean()
        }

  @impl true
  def init(_opts) do
    Metrics.connection(+1)
    {:ok, _} = Registry.register(Wrinkle.Sockets, :socket, nil)
    now = System.monotonic_time(:millisecond)

    {:ok,
     restart_deadline(%__MODULE__{
       pushes: :atomics.new(1, []),
       bucket: TokenBucket.new(now),
       seen_at: now
     })}
  end

  @impl true
  def handle_in({data, [opcode: :binary]}, %__MODULE__{} = state) do
    now = System.monotonic_time(:millisecond)
    state = %__MODULE__{state | seen_at: now}

    case decode(data) do
      {:ok, %Frame{kind: {kind, _} = body}} when kind in [:request, :ack, :ping, :pong] ->
        {result, bucket} = TokenBucket.take(state.bucket, now)
        inbound(body, result, %__MODULE__{state | bucket: bucket})

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
  def handle_control(_frame, %__MODULE__{} = state) do
    {:ok, %__MODULE__{state | seen_at: System.monotonic_time(:millisecond)}}
  end

  @impl true
  def handle_info({ref, result}, %__MODULE__{tasks: tasks} = state) when is_map_key(tasks, ref) do
    Process.demonitor(ref, [:flush])
    {task, tasks} = Map.pop(tasks, ref)
    finished(task, result, %__MODULE__{state | tasks: tasks})
  end

  def handle_info({:DOWN, ref, :process, _pid, _reason}, %__MODULE__{tasks: tasks} = state)
      when is_map_key(tasks, ref) do
    {task, tasks} = Map.pop(tasks, ref)
    crashed(task, %__MODULE__{state | tasks: tasks})
  end

  def handle_info({:DOWN, ref, :process, _pid, _reason}, %{push: ref} = state) do
    {:stop, :normal, {1011, "push subscriptions lost"}, state}
  end

  def handle_info(
        {:subscribed, _user_id},
        %__MODULE__{connect: {:subscribing, id, response, started, _ref}} = state
      ) do
    respond({id, "connect", started}, response, %__MODULE__{state | connect: :done})
  end

  # Not connected after all: Connect may be retried, within a fresh deadline.
  def handle_info(
        {:subscribe_deadline, ref},
        %__MODULE__{connect: {:subscribing, id, _, started, ref}, device: {user_id, device_id}} =
          state
      ) do
    Logger.warning("push subscription of user #{Push.hex(user_id)} not ready, connect refused")
    Push.detach()
    Presence.delete(user_id, device_id)
    Process.demonitor(state.push, [:flush])
    response = error(id, :ERROR_CODE_UNAVAILABLE, "push channel unavailable")

    state =
      restart_deadline(%__MODULE__{
        state
        | device: nil,
          connect: :none,
          push: nil,
          ack: AckWriter.drop(state.ack)
      })

    respond({id, "connect", started}, response, state)
  end

  # Taken from the mailbox and written at once; the count is what is still
  # queued behind this one.
  def handle_info({:push, frame}, state) do
    if :atomics.sub_get(state.pushes, 1, 1) >= @outbound_queue do
      {:stop, :normal, {1013, "too slow"}, state}
    else
      Metrics.pushed()
      {:push, {:binary, frame}, state}
    end
  end

  def handle_info({:signed_out, devices}, %{device: {_, device_id}} = state) do
    if devices == :all or device_id in devices do
      {:stop, :normal, @signed_out, state}
    else
      {:ok, state}
    end
  end

  def handle_info({:connect_deadline, ref}, %{device: nil, deadline: ref} = state) do
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

  def handle_info({:drain, grace}, %__MODULE__{} = state) do
    Process.send_after(self(), :drain_deadline, grace)
    {:ok, %__MODULE__{state | draining: true}}
  end

  def handle_info(:drain_deadline, state), do: {:stop, :normal, @shutting_down, state}

  def handle_info(:ack_due, %__MODULE__{} = state) do
    {action, acks} = AckWriter.due(state.ack, System.monotonic_time(:millisecond))
    {:ok, write_ack(action, %__MODULE__{state | ack: acks})}
  end

  def handle_info(_other, state), do: {:ok, state}

  @impl true
  def terminate(_reason, state) do
    Metrics.connection(-1)
    # At once rather than at exit, which waits for the closing handshake.
    Registry.unregister(Wrinkle.Sockets, :socket)

    with {user_id, device_id} <- state.device do
      Push.detach()
      Presence.delete(user_id, device_id)
      final_ack(state)
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

  # One Connect per connection, also while the first is still forwarded; no
  # Register or SignIn once one is.
  defp inbound({:request, %Request{body: {kind, _}} = request}, :ok, %{connect: c} = state)
       when kind in [:register, :sign_in, :connect] and c != :none do
    refuse(
      request,
      :ERROR_CODE_FAILED_PRECONDITION,
      "already connected",
      state,
      :ERROR_REASON_ALREADY_CONNECTED
    )
  end

  defp inbound({:request, %Request{body: {kind, _}} = request}, :ok, %{sign_ins: n} = state)
       when kind in [:register, :sign_in] and n >= @max_sign_ins do
    message = "at most #{@max_sign_ins} sign-ins per connection"
    refuse(request, :ERROR_CODE_RESOURCE_EXHAUSTED, message, state)
  end

  defp inbound({:request, %Request{id: id} = request}, :ok, %__MODULE__{} = state) do
    device = state.device
    task = Task.Supervisor.async_nolink(Wrinkle.Tasks, fn -> dispatch(device, request) end)
    type = request_type(request)
    entry = {:request, id, type, System.monotonic_time()}
    connect = if type == "connect", do: :pending, else: state.connect
    sign_ins = if type in ["register", "sign_in"], do: state.sign_ins + 1, else: state.sign_ins

    {:ok,
     %__MODULE__{
       state
       | tasks: Map.put(state.tasks, task.ref, entry),
         in_flight: state.in_flight + 1,
         connect: connect,
         sign_ins: sign_ins
     }}
  end

  defp inbound({:ack, ack}, :ok, %__MODULE__{device: {_, _}} = state) do
    now = System.monotonic_time(:millisecond)
    {action, acks} = AckWriter.ack(state.ack, ack.cursor, now, ack_interval())
    {:ok, write_ack(action, %__MODULE__{state | ack: acks})}
  end

  defp inbound({:ack, _}, :ok, state), do: {:ok, state}
  defp inbound({:ping, _}, :ok, state), do: {:push, encode({:pong, %Pong{}}), state}
  defp inbound({:pong, _}, :ok, state), do: {:ok, state}

  # Answered at once, without the services; counted with a duration of zero.
  defp refuse(
         %Request{id: id} = request,
         code,
         message,
         state,
         reason \\ :ERROR_REASON_UNSPECIFIED
       ) do
    response = error(id, code, message, reason)
    Metrics.request(request_type(request), response_code(response), 0)
    {:push, encode({:response, response}), state}
  end

  # A successful Connect binds the connection to the device; its response
  # waits until the user's push channel is subscribed.
  defp finished(
         {:request, id, _type, started},
         {response, {user_id, device_id} = device},
         %__MODULE__{} = state
       ) do
    Push.attach(user_id, state.pushes)
    Presence.put(user_id, device_id)
    ref = make_ref()
    Process.send_after(self(), {:subscribe_deadline, ref}, @subscribe_timeout)

    {:ok,
     %__MODULE__{
       state
       | device: device,
         connect: {:subscribing, id, response, started, ref},
         push: Process.monitor(Push)
     }}
  end

  defp finished({:request, id, type, started}, {response, nil}, state) do
    respond({id, type, started}, response, after_request(type, response, state))
  end

  defp finished({:ack, _}, result, state) do
    log_ack(result)
    ack_done(state)
  end

  # A task that crashed: its request is answered, an ack write is done.
  defp crashed({:request, id, type, started}, state) do
    response = error(id, :ERROR_CODE_INTERNAL, "internal error")
    respond({id, type, started}, response, after_request(type, response, state))
  end

  defp crashed({:ack, _}, state), do: ack_done(state)

  # A Connect that failed may be retried. Hashing a password may have taken
  # a while, so a successful Register or SignIn gives Connect a fresh
  # deadline; a failed one does not, so it cannot hold the connection open.
  # One refused as invalid gives its sign-in back.
  defp after_request("connect", _response, %__MODULE__{} = state),
    do: %__MODULE__{state | connect: :none}

  defp after_request(type, response, %__MODULE__{} = state)
       when type in ["register", "sign_in"] do
    case response do
      %Response{body: {:error, %WireError{code: :ERROR_CODE_INVALID_ARGUMENT}}} ->
        %__MODULE__{state | sign_ins: state.sign_ins - 1}

      %Response{body: {:error, _}} ->
        state

      _ ->
        restart_deadline(state)
    end
  end

  defp after_request(_type, _response, state), do: state

  defp restart_deadline(%__MODULE__{} = state) do
    ref = make_ref()
    Process.send_after(self(), {:connect_deadline, ref}, @connect_deadline)
    %__MODULE__{state | deadline: ref}
  end

  defp respond({_id, type, started}, response, %__MODULE__{} = state) do
    Metrics.request(type, response_code(response), System.monotonic_time() - started)
    state = %__MODULE__{state | in_flight: state.in_flight - 1}
    frame = encode({:response, response})

    if state.draining and state.in_flight == 0 do
      {:stop, :normal, @shutting_down, frame, state}
    else
      {:push, frame, state}
    end
  end

  defp ack_done(%__MODULE__{} = state) do
    now = System.monotonic_time(:millisecond)
    {action, ack} = AckWriter.done(state.ack, now, ack_interval())
    {:ok, write_ack(action, %__MODULE__{state | ack: ack})}
  end

  defp write_ack(:none, state), do: state

  defp write_ack({:schedule, delay}, state) do
    Process.send_after(self(), :ack_due, delay)
    state
  end

  defp write_ack({:write, cursor}, %__MODULE__{device: {user_id, device_id}} = state) do
    task =
      Task.Supervisor.async_nolink(Wrinkle.AckWrites, fn ->
        Backend.ack(user_id, device_id, cursor)
      end)

    %__MODULE__{state | tasks: Map.put(state.tasks, task.ref, {:ack, task.pid})}
  end

  # The pending cursor, written by a task that outlives the connection, after
  # the write in flight; a scheduled write dies with this process.
  defp final_ack(%__MODULE__{device: device} = state) do
    case AckWriter.close(state.ack) do
      {nil, _, _} -> :ok
      {cursor, false, _} -> final_ack(device, nil, cursor)
      {cursor, true, _} -> final_ack(device, Enum.find_value(state.tasks, &ack_task/1), cursor)
    end
  end

  defp final_ack({user_id, device_id}, in_flight, cursor) do
    write = fn -> log_ack(Backend.ack(user_id, device_id, cursor)) end

    Task.Supervisor.start_child(Wrinkle.AckWrites, fn ->
      AckWriter.final_write(in_flight, write)
    end)
  end

  defp ack_task({_ref, {:ack, pid}}), do: pid
  defp ack_task(_), do: nil

  defp log_ack({:error, reason}), do: Logger.warning("ack failed: #{inspect(reason)}")
  defp log_ack(_), do: :ok

  defp ack_interval, do: Application.get_env(:wrinkle, :ack_interval, 60_000)

  defp encode(kind), do: {:binary, Frame.encode(%Frame{kind: kind})}

  # Runs in the task: the response and, after a successful Connect, the
  # device to bind to.
  defp dispatch(device, %Request{id: id} = request) do
    case {request.body, device} do
      {{kind, _}, nil} when kind in [:register, :sign_in, :connect] -> forward({<<>>, 0}, request)
      {_, nil} -> {error(id, :ERROR_CODE_UNAUTHENTICATED, "connect first"), nil}
      {_, device} -> forward(device, request)
    end
  end

  defp forward({user_id, device_id}, %Request{id: id} = request) do
    case Backend.call(user_id, device_id, request) do
      {:ok,
       %Response{body: {:connect, %{user: %{user_id: <<_::binary-size(16)>> = user_id}} = c}} =
           response} ->
        {response, {user_id, c.device_id}}

      {:ok, %Response{body: {:connect, _}}} ->
        {error(id, :ERROR_CODE_INTERNAL, "connect returned no user id"), nil}

      {:ok, response} ->
        {response, nil}

      {:error, reason} ->
        Logger.error("backend call failed: #{inspect(reason)}")
        {error(id, :ERROR_CODE_UNAVAILABLE, "services unavailable"), nil}
    end
  end

  defp error(id, code, message, reason \\ :ERROR_REASON_UNSPECIFIED) do
    error = %WireError{code: code, message: message, reason: reason}
    %Response{request_id: id, body: {:error, error}}
  end

  defp request_type(%Request{body: {type, _}}), do: Atom.to_string(type)
  defp request_type(_), do: "unknown"

  defp response_code(%Response{body: {:error, %WireError{code: code}}}), do: Atom.to_string(code)
  defp response_code(_), do: "ok"
end
