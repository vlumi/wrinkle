defmodule Wrinkle.Push do
  @moduledoc """
  Pushes from fan-out workers through Valkey: one channel per user, subscribed
  while this node holds one of the user's connections, delivered to every
  connection process of that user.

  This process keeps the desired state, the connections of each user, and is
  the only subscriber. Redix.PubSub applies it in call order without waiting
  on Valkey, so an unsubscribe racing a new attach ends subscribed, and after
  a reconnection it resubscribes every channel still wanted. A connection
  that attaches gets `{:subscribed, user_id}` once Valkey has confirmed the
  channel, before any push for the user.
  """
  use GenServer
  import Bitwise
  require Logger

  @pubsub Wrinkle.ValkeyPubSub
  @channel_prefix "push:"

  def start_link(_), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @doc "Attaches the calling connection process to the user's pushes."
  def attach(user_id), do: GenServer.cast(__MODULE__, {:attach, self(), user_id})

  @doc "Detaches the calling connection process; the last one unsubscribes."
  def detach, do: GenServer.cast(__MODULE__, {:detach, self()})

  @doc "The lowercase hex of a user id, naming the user in Valkey keys and channels."
  def hex(user_id), do: Base.encode16(user_id, case: :lower)

  @doc "Wraps an already serialized Event in a Frame without decoding it: Frame.event is field 3."
  def event_frame(payload) do
    <<0x1A, varint(byte_size(payload))::binary, payload::binary>>
  end

  @doc """
  Decodes a message on a user's push channel, a wrinkle.backend.v1.Push,
  without decoding its event: `{:event, frame}` with the event wrapped in a
  Frame, `{:signed_out, device_ids | :all}`, or `:error`. A Push of only an
  event differs from the Frame that carries it only in the field number,
  Push.event being field 1, so the common case swaps the tag.
  """
  def decode(<<0x0A, rest::binary>> = payload) do
    case read_varint(rest, 0, 0) do
      {len, event} when byte_size(event) == len -> {:event, <<0x1A, rest::binary>>}
      _ -> decode_push(payload)
    end
  end

  def decode(payload), do: decode_push(payload)

  defp decode_push(payload) do
    case Wrinkle.Backend.V1.Push.decode(payload) do
      %{kind: {:event, event}} -> {:event, event_frame(event)}
      %{kind: {:signed_out, %{all_devices: true}}} -> {:signed_out, :all}
      %{kind: {:signed_out, %{device_ids: ids}}} -> {:signed_out, ids}
      _ -> :error
    end
  rescue
    Protobuf.DecodeError -> :error
  end

  defp read_varint(<<1::1, b::7, rest::binary>>, shift, acc) when shift < 63,
    do: read_varint(rest, shift + 7, acc ||| b <<< shift)

  defp read_varint(<<0::1, b::7, rest::binary>>, shift, acc), do: {acc ||| b <<< shift, rest}
  defp read_varint(_, _, _), do: :error

  defp varint(n) when n < 128, do: <<n>>
  defp varint(n), do: <<1::1, n &&& 0x7F::7, varint(n >>> 7)::binary>>

  # users: user id -> %{pids, confirmed}, present while subscribed;
  # conns: connection pid -> {monitor, user id}.
  @impl true
  def init(_) do
    # Subscriptions live in the Redix.PubSub process; if it restarts, so does
    # this one, and the connections close.
    {:ok, %{users: %{}, conns: %{}, pubsub: Process.monitor(@pubsub)}}
  end

  @impl true
  def handle_cast({:attach, pid, user_id}, %{conns: conns} = state)
      when not is_map_key(conns, pid) do
    user =
      case state.users do
        %{^user_id => user} ->
          user

        _ ->
          {:ok, _} = Redix.PubSub.subscribe(@pubsub, channel(user_id), self())
          %{pids: MapSet.new(), confirmed: false}
      end

    if user.confirmed, do: send(pid, {:subscribed, user_id})

    {:noreply,
     %{
       state
       | users: Map.put(state.users, user_id, %{user | pids: MapSet.put(user.pids, pid)}),
         conns: Map.put(conns, pid, {Process.monitor(pid), user_id})
     }}
  end

  def handle_cast({:attach, _pid, _user_id}, state), do: {:noreply, state}

  def handle_cast({:detach, pid}, state), do: {:noreply, remove(state, pid)}

  @impl true
  def handle_info(
        {:redix_pubsub, _pid, _ref, :message,
         %{channel: @channel_prefix <> id, payload: payload}},
        state
      ) do
    with {:ok, user_id} <- Base.decode16(id, case: :lower),
         %{pids: pids} <- state.users[user_id] do
      case decode(payload) do
        {:event, frame} ->
          for pid <- pids, do: send(pid, {:push, frame})

        {:signed_out, devices} ->
          for pid <- pids, do: send(pid, {:signed_out, devices})

        :error ->
          Logger.warning("undecodable push for user #{id} dropped")
      end
    end

    {:noreply, state}
  end

  def handle_info(
        {:redix_pubsub, _pid, _ref, :subscribed, %{channel: @channel_prefix <> id}},
        state
      ) do
    case Base.decode16(id, case: :lower) do
      {:ok, user_id} -> confirm(state, user_id)
      :error -> {:noreply, state}
    end
  end

  # Redix resubscribes after reconnecting and confirms each channel again.
  def handle_info({:redix_pubsub, _pid, _ref, :disconnected, _}, state) do
    {:noreply,
     %{state | users: Map.new(state.users, fn {id, u} -> {id, %{u | confirmed: false}} end)}}
  end

  def handle_info({:redix_pubsub, _pid, _ref, _kind, _}, state), do: {:noreply, state}

  def handle_info({:DOWN, ref, :process, _pid, reason}, %{pubsub: ref} = state) do
    {:stop, {:pubsub_down, reason}, state}
  end

  def handle_info({:DOWN, _ref, :process, pid, _reason}, state) do
    {:noreply, remove(state, pid)}
  end

  defp confirm(state, user_id) do
    case state.users do
      %{^user_id => %{confirmed: false} = user} ->
        for pid <- user.pids, do: send(pid, {:subscribed, user_id})
        {:noreply, put_in(state.users[user_id], %{user | confirmed: true})}

      _ ->
        {:noreply, state}
    end
  end

  defp remove(state, pid) do
    case Map.pop(state.conns, pid) do
      {nil, _} ->
        state

      {{ref, user_id}, conns} ->
        Process.demonitor(ref, [:flush])
        user = state.users[user_id]
        pids = MapSet.delete(user.pids, pid)

        users =
          if MapSet.size(pids) == 0 do
            unsubscribe(user_id)
            Map.delete(state.users, user_id)
          else
            Map.put(state.users, user_id, %{user | pids: pids})
          end

        %{state | users: users, conns: conns}
    end
  end

  defp unsubscribe(user_id) do
    channel = channel(user_id)
    :ok = Redix.PubSub.unsubscribe(@pubsub, channel, self())
    # A confirmation Redix sent before the unsubscribe is for the subscription
    # it ended; it must not confirm a subscription that follows.
    flush_subscribed(channel)
  end

  defp flush_subscribed(channel) do
    receive do
      {:redix_pubsub, _pid, _ref, :subscribed, %{channel: ^channel}} -> flush_subscribed(channel)
    after
      0 -> :ok
    end
  end

  defp channel(user_id), do: @channel_prefix <> hex(user_id)
end
