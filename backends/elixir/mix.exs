defmodule Wrinkle.MixProject do
  use Mix.Project

  # The Elixir gateway: WebSocket connections on the BEAM, one process each,
  # forwarding requests to a services process over the internal RPC.
  def project do
    [
      app: :wrinkle,
      version: "0.1.0",
      elixir: "~> 1.20",
      start_permanent: Mix.env() == :prod,
      deps: deps()
    ]
  end

  def application do
    [
      extra_applications: [:logger],
      mod: {Wrinkle.Application, []}
    ]
  end

  defp deps do
    [
      {:bandit, "~> 1.12"},
      {:websock_adapter, "~> 0.6"},
      {:plug, "~> 1.20"},
      {:protobuf, "~> 0.17"},
      {:grpc, "~> 1.0"},
      {:redix, "~> 1.9"},
      {:mint, "~> 1.11"}
    ]
  end
end
