defmodule Wrinkle.Router do
  @moduledoc "HTTP: the WebSocket endpoint, health and metrics."
  use Plug.Router
  import Bitwise

  alias Wrinkle.Drain

  @max_frame 64 * 1024
  # A client frame of 64 KiB has a 14-byte header; Bandit counts it in.
  @max_frame_header 14
  # Only a backstop for a closed connection whose peer never completes the
  # closing handshake: Wrinkle.Keepalive closes idle ones well before.
  @read_timeout 150_000

  plug(:loopback_debug)
  plug(:match)
  plug(:dispatch)

  get "/v1/ws" do
    if Drain.draining?() do
      send_resp(conn, 503, "shutting down\n")
    else
      conn
      |> WebSockAdapter.upgrade(Wrinkle.Socket, [],
        timeout: @read_timeout,
        max_frame_size: @max_frame + @max_frame_header,
        max_fragmented_message_size: @max_frame,
        validate_text_frames: false
      )
      |> halt()
    end
  end

  get "/healthz" do
    if Drain.draining?(),
      do: send_resp(conn, 503, "shutting down\n"),
      else: send_resp(conn, 200, "ok\n")
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

  defp loopback_debug(%{path_info: ["debug" | _]} = conn, _opts) do
    if loopback?(conn.remote_ip), do: conn, else: conn |> send_resp(403, "forbidden\n") |> halt()
  end

  defp loopback_debug(conn, _opts), do: conn

  defp loopback?({127, _, _, _}), do: true
  defp loopback?({0, 0, 0, 0, 0, 0, 0, 1}), do: true
  defp loopback?({0, 0, 0, 0, 0, 0xFFFF, high, _}), do: high >>> 8 == 127
  defp loopback?(_), do: false

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
