defmodule Wrinkle.Push do
  @moduledoc """
  Pushes from fan-out workers through Valkey: one channel per user, subscribed
  while this node holds one of the user's connections, delivered to every
  connection process of that user.
  """
  use GenServer
  import Bitwise

  @channel_prefix "push:"

  def start_link(_), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @doc "A connection of the user is open on this node, registered in Wrinkle.Connections."
  def attach(user_id), do: GenServer.call(__MODULE__, {:attach, user_id})

  @doc "A connection of the user closed; unsubscribes after the last."
  def detach(user_id), do: GenServer.cast(__MODULE__, {:detach, user_id})

  @doc "Wraps an already serialized Event in a Frame without decoding it: Frame.event is field 3."
  def event_frame(payload) do
    <<0x1A, varint(byte_size(payload))::binary, payload::binary>>
  end

  defp varint(n) when n < 128, do: <<n>>
  defp varint(n), do: <<1::1, n &&& 0x7F::7, varint(n >>> 7)::binary>>

  @impl true
  def init(_) do
    {:ok, %{refs: %{}}}
  end

  @impl true
  def handle_call({:attach, user_id}, _from, state) do
    if Map.has_key?(state.refs, user_id) do
      {:reply, :ok, state}
    else
      {:ok, ref} = Redix.PubSub.subscribe(Wrinkle.ValkeyPubSub, channel(user_id), self())
      {:reply, :ok, %{state | refs: Map.put(state.refs, user_id, ref)}}
    end
  end

  @impl true
  def handle_cast({:detach, user_id}, state) do
    if Registry.count_match(Wrinkle.Connections, user_id, :_) == 0 and
         Map.has_key?(state.refs, user_id) do
      Redix.PubSub.unsubscribe(Wrinkle.ValkeyPubSub, channel(user_id), self())
      {:noreply, %{state | refs: Map.delete(state.refs, user_id)}}
    else
      {:noreply, state}
    end
  end

  @impl true
  def handle_info(
        {:redix_pubsub, _pid, _ref, :message,
         %{channel: @channel_prefix <> id, payload: payload}},
        state
      ) do
    frame = event_frame(payload)

    Registry.dispatch(Wrinkle.Connections, String.to_integer(id), fn entries ->
      for {pid, _} <- entries, do: send(pid, {:push, frame})
    end)

    {:noreply, state}
  end

  def handle_info({:redix_pubsub, _pid, _ref, _kind, _}, state), do: {:noreply, state}

  defp channel(user_id), do: @channel_prefix <> Integer.to_string(user_id)
end
