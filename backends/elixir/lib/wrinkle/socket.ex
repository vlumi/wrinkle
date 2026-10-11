defmodule Wrinkle.Socket do
  @moduledoc """
  One device's WebSocket connection: a process. Frames in are decoded and
  requests forwarded to the services in a task each, so a slow request does
  not block pushes; responses and pushes are written from this process.
  """
  @behaviour WebSock
  require Logger

  alias Wrinkle.{Backend, Metrics, Presence, Push}
  alias Wrinkle.V1.{Frame, Pong, Request, Response}
  alias Wrinkle.V1.Error, as: WireError

  # device is nil until Connect succeeds, then {user_id, device_id}.
  @impl true
  def init(_opts) do
    Metrics.connection(+1)
    {:ok, %{device: nil}}
  end

  @impl true
  def handle_in({data, [opcode: :binary]}, state) do
    case Frame.decode(data) do
      %Frame{kind: {:request, request}} ->
        socket = self()
        Task.start(fn -> send(socket, {:response, handle_request(state.device, request)}) end)
        {:ok, state}

      %Frame{kind: {:ack, ack}} ->
        with {user_id, device_id} <- state.device do
          Task.start(fn -> Backend.ack(user_id, device_id, ack.cursor) end)
        end

        {:ok, state}

      %Frame{kind: {:ping, _}} ->
        {:push, encode({:pong, %Pong{}}), state}

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
    {:push, encode({:response, response}), attach(state, connected)}
  end

  def handle_info({:push, frame}, state) do
    Metrics.pushed()
    {:push, {:binary, frame}, state}
  end

  def handle_info(_other, state), do: {:ok, state}

  @impl true
  def terminate(_reason, state) do
    Metrics.connection(-1)

    with {user_id, device_id} <- state.device do
      Push.detach(user_id)
      Presence.delete(user_id, device_id)
    end

    :ok
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
