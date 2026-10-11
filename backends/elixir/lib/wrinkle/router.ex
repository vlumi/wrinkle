defmodule Wrinkle.Router do
  @moduledoc "HTTP: the WebSocket endpoint, health and metrics."
  use Plug.Router

  plug(:match)
  plug(:dispatch)

  get "/v1/ws" do
    conn
    |> WebSockAdapter.upgrade(Wrinkle.Socket, %{remote: conn.remote_ip},
      timeout: :infinity,
      max_frame_size: 65_536
    )
    |> halt()
  end

  get "/healthz" do
    send_resp(conn, 200, "ok\n")
  end

  get "/metrics" do
    conn
    |> put_resp_content_type("text/plain")
    |> send_resp(200, Wrinkle.Metrics.render())
  end

  get "/debug/queues" do
    send_resp(conn, 200, longest_queues(8) <> "\n")
  end

  post "/debug/gc" do
    for pid <- Process.list(), do: :erlang.garbage_collect(pid)
    send_resp(conn, 200, "ok\n")
  end

  match _ do
    send_resp(conn, 404, "not found\n")
  end

  # The processes with the longest mailboxes, for finding a bottleneck.
  defp longest_queues(count) do
    Process.list()
    |> Enum.map(
      &Process.info(&1, [:message_queue_len, :registered_name, :initial_call, :current_function])
    )
    |> Enum.map(fn info ->
      {info[:message_queue_len] || 0, inspect(info[:registered_name]),
       inspect(info[:initial_call]), inspect(info[:current_function])}
    end)
    |> Enum.sort_by(&elem(&1, 0), :desc)
    |> Enum.take(count)
    |> Enum.map_join("\n", fn {n, name, init, cur} -> "#{n} #{name} #{init} #{cur}" end)
  end
end
