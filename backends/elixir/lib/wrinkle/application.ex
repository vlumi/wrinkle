defmodule Wrinkle.Application do
  @moduledoc false
  use Application
  require Logger

  @impl true
  def start(_type, _args) do
    Wrinkle.Metrics.init()
    port = Application.fetch_env!(:wrinkle, :port)
    valkey = valkey_options()

    children = [
      {Registry, keys: :duplicate, name: Wrinkle.Connections},
      {Redix, valkey ++ [name: Wrinkle.Valkey]},
      %{
        id: Redix.PubSub,
        start: {Redix.PubSub, :start_link, [valkey ++ [name: Wrinkle.ValkeyPubSub]]}
      },
      Wrinkle.Backend,
      Wrinkle.Push,
      {Bandit, plug: Wrinkle.Router, port: port, http_options: [log_protocol_errors: false]}
    ]

    Logger.info(
      "gateway listening on #{port}, services at #{Application.fetch_env!(:wrinkle, :services)}"
    )

    Supervisor.start_link(children, strategy: :one_for_one, name: Wrinkle.Supervisor)
  end

  defp valkey_options do
    [host, port] = String.split(Application.fetch_env!(:wrinkle, :valkey), ":")
    [host: host, port: String.to_integer(port)]
  end
end
