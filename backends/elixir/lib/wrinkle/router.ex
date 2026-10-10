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

  post "/debug/gc" do
    for pid <- Process.list(), do: :erlang.garbage_collect(pid)
    send_resp(conn, 200, "ok\n")
  end

  match _ do
    send_resp(conn, 404, "not found\n")
  end
end
