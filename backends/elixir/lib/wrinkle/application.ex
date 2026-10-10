defmodule Wrinkle.Application do
  @moduledoc false
  use Application

  @impl true
  def start(_type, _args) do
    Wrinkle.Metrics.init()
    port = Application.fetch_env!(:wrinkle, :port)
    [host, valkey_port] = String.split(Application.fetch_env!(:wrinkle, :valkey), ":")

    children = [
      {Registry, keys: :duplicate, name: Wrinkle.Connections},
      {Redix, host: host, port: String.to_integer(valkey_port), name: Wrinkle.Valkey},
      %{
        id: Redix.PubSub,
        start:
          {Redix.PubSub, :start_link,
           [[host: host, port: String.to_integer(valkey_port), name: Wrinkle.ValkeyPubSub]]}
      },
      Wrinkle.Backend,
      Wrinkle.Push,
      {Bandit, plug: Wrinkle.Router, port: port, http_options: [log_protocol_errors: false]}
    ]

    require Logger

    Logger.info(
      "gateway listening on #{port}, services at #{Application.fetch_env!(:wrinkle, :services)}"
    )

    Supervisor.start_link(children, strategy: :one_for_one, name: Wrinkle.Supervisor)
  end
end
