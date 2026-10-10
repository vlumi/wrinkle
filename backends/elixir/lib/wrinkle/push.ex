defmodule Wrinkle.Push do
  @moduledoc """
  Pushes from fan-out workers through Valkey: one channel per user, subscribed
  while this node holds one of the user's connections, delivered to every
  connection process of that user. Also keeps the presence hash.
  """
  use GenServer

  def start_link(_), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @doc "A connection of the user is open on this node."
  def attach(user_id, device_id) do
    GenServer.call(__MODULE__, {:attach, user_id})
    node = Application.fetch_env!(:wrinkle, :node)

    Redix.noreply_command(Wrinkle.Valkey, [
      "HSET",
      "presence:#{user_id}",
      Integer.to_string(device_id),
      node
    ])
  end

  def detach(user_id, device_id) do
    GenServer.cast(__MODULE__, {:detach, user_id})

    Redix.noreply_command(Wrinkle.Valkey, [
      "HDEL",
      "presence:#{user_id}",
      Integer.to_string(device_id)
    ])
  end

  @impl true
  def init(_) do
    {:ok, %{refs: %{}}}
  end

  @impl true
  def handle_call({:attach, user_id}, _from, state) do
    if Map.has_key?(state.refs, user_id) do
      {:reply, :ok, state}
    else
      {:ok, ref} = Redix.PubSub.subscribe(Wrinkle.ValkeyPubSub, "push:#{user_id}", self())
      {:reply, :ok, %{state | refs: Map.put(state.refs, user_id, ref)}}
    end
  end

  @impl true
  def handle_cast({:detach, user_id}, state) do
    if Registry.count_match(Wrinkle.Connections, user_id, :_) == 0 and
         Map.has_key?(state.refs, user_id) do
      Redix.PubSub.unsubscribe(Wrinkle.ValkeyPubSub, "push:#{user_id}", self())
      {:noreply, %{state | refs: Map.delete(state.refs, user_id)}}
    else
      {:noreply, state}
    end
  end

  @impl true
  def handle_info(
        {:redix_pubsub, _pid, _ref, :message, %{channel: "push:" <> id, payload: payload}},
        state
      ) do
    user_id = String.to_integer(id)
    frame = Wrinkle.Socket.event_frame(payload)

    Registry.dispatch(Wrinkle.Connections, user_id, fn entries ->
      for {pid, _} <- entries, do: send(pid, {:push, frame})
    end)

    {:noreply, state}
  end

  def handle_info({:redix_pubsub, _pid, _ref, _kind, _}, state), do: {:noreply, state}
end
