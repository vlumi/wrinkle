defmodule Wrinkle.Application do
  @moduledoc false
  use Application
  require Logger

  # A write blocked this long on a device that does not read closes it.
  @write_timeout 10_000

  @impl true
  def start(_type, _args) do
    Wrinkle.Metrics.init()
    {ip, port} = Application.fetch_env!(:wrinkle, :listen)
    valkey = valkey_options()
    instance = Wrinkle.Presence.new_instance(Application.fetch_env!(:wrinkle, :node))

    children = [
      {Registry, keys: :duplicate, name: Wrinkle.Sockets, partitions: System.schedulers_online()},
      {Redix, valkey ++ [name: Wrinkle.Valkey]},
      %{
        id: Redix.PubSub,
        start: {Redix.PubSub, :start_link, [valkey ++ [name: Wrinkle.ValkeyPubSub]]}
      },
      Wrinkle.Presence,
      {PartitionSupervisor, child_spec: Wrinkle.Presence.Writer, name: Wrinkle.PresenceWriters},
      Wrinkle.Backend,
      Wrinkle.Push,
      Wrinkle.Keepalive,
      {Task.Supervisor, name: Wrinkle.Tasks},
      {Bandit,
       plug: Wrinkle.Router,
       ip: ip,
       port: port,
       http_options: [log_protocol_errors: false],
       thousand_island_options: [
         # Connections are closed by the drain; this bounds what is left.
         shutdown_timeout: 1_000,
         transport_options: [send_timeout: @write_timeout]
       ]}
    ]

    Logger.info(
      "gateway #{instance} listening on #{:inet.ntoa(ip)}:#{port}, services at #{Application.fetch_env!(:wrinkle, :services)}"
    )

    Supervisor.start_link(children, strategy: :one_for_one, name: Wrinkle.Supervisor)
  end

  # SIGTERM stops the application (the VM's default), which drains first.
  @impl true
  def prep_stop(state) do
    Wrinkle.Drain.run()
    state
  end

  defp valkey_options do
    [host, port] = String.split(Application.fetch_env!(:wrinkle, :valkey), ":")
    [host: host, port: String.to_integer(port)]
  end
end
