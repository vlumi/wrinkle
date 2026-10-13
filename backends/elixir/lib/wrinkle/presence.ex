defmodule Wrinkle.Presence do
  @moduledoc """
  Which gateway instance holds each device's connection: one Valkey hash per
  user, device to instance id. An instance is alive while its key
  gateway:<instance id> exists; this process refreshes it, so the entries of
  a gateway that died without draining stop counting once the key expires.
  """
  use GenServer
  require Logger

  @ttl_seconds 90
  @refresh 30_000
  @retry 1_000

  # Removes a device's entry only if it still names this instance: a device
  # that reconnected elsewhere keeps its new entry.
  @delete_if_ours """
  if redis.call('HGET', KEYS[1], ARGV[1]) == ARGV[2] then return redis.call('HDEL', KEYS[1], ARGV[1]) end return 0
  """

  @spec start_link(term()) :: GenServer.on_start()
  def start_link(_), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @doc "This process's instance id, made at startup: `<node>/<16 random hex chars>`."
  @spec instance() :: String.t()
  def instance, do: :persistent_term.get({__MODULE__, :instance})

  @spec new_instance(String.t()) :: String.t()
  def new_instance(node) do
    id = node <> "/" <> Base.encode16(:crypto.strong_rand_bytes(8), case: :lower)
    :persistent_term.put({__MODULE__, :instance}, id)
    id
  end

  @doc "Records the device as connected here, without waiting for Valkey."
  @spec put(binary(), non_neg_integer()) :: :ok
  def put(user_id, device_id) do
    write(user_id, ["HSET", key(user_id), Integer.to_string(device_id), instance()])
  end

  @doc "Removes the device's entry if it names this instance, without waiting for Valkey."
  @spec delete(binary(), non_neg_integer()) :: :ok
  def delete(user_id, device_id) do
    write(user_id, [
      "EVAL",
      @delete_if_ours,
      "1",
      key(user_id),
      Integer.to_string(device_id),
      instance()
    ])
  end

  # A user's writes go to one writer, in order; different users' in parallel.
  defp write(user_id, command) do
    GenServer.cast({:via, PartitionSupervisor, {Wrinkle.PresenceWriters, user_id}}, command)
  end

  @doc "Stops refreshing this instance's key and deletes it, on drain."
  @spec stop() :: :ok
  def stop, do: GenServer.call(__MODULE__, :stop)

  defp key(user_id), do: "presence:" <> Wrinkle.Push.hex(user_id)
  defp instance_key, do: "gateway:" <> instance()

  @impl true
  def init(_) do
    send(self(), :refresh)
    {:ok, :alive}
  end

  @impl true
  def handle_info(:refresh, :alive) do
    case Redix.command(Wrinkle.Valkey, ["SET", instance_key(), "1", "EX", "#{@ttl_seconds}"]) do
      {:ok, _} ->
        Process.send_after(self(), :refresh, @refresh)

      {:error, reason} ->
        Logger.warning("refreshing #{instance_key()}: #{inspect(reason)}")
        Process.send_after(self(), :refresh, @retry)
    end

    {:noreply, :alive}
  end

  def handle_info(:refresh, :stopped), do: {:noreply, :stopped}

  @impl true
  def handle_call(:stop, _from, _state) do
    # Also waits for the connections' deletes, sent earlier on the same connection.
    Redix.command(Wrinkle.Valkey, ["DEL", instance_key()])
    {:reply, :ok, :stopped}
  end
end

defmodule Wrinkle.Presence.Writer do
  @moduledoc "Sends presence writes to Valkey, so a connection never waits on them."
  use GenServer
  require Logger

  # One per scheduler: noreply_command still waits for a round trip (the reply
  # to its CLIENT REPLY ON), so a single writer would be bound by Valkey's latency.
  @spec start_link(term()) :: GenServer.on_start()
  def start_link(_), do: GenServer.start_link(__MODULE__, nil)

  @impl true
  def init(nil), do: {:ok, nil}

  @impl true
  def handle_cast(command, state) do
    with {:error, reason} <- Redix.noreply_command(Wrinkle.Valkey, command) do
      Logger.warning("presence update failed: #{inspect(reason)}")
    end

    {:noreply, state}
  end
end
