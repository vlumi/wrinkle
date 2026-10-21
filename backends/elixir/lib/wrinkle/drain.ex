defmodule Wrinkle.Drain do
  @moduledoc """
  Graceful shutdown, run as the application stops (SIGTERM stops it): health
  checks and upgrades get 503, every connection closes with 1001 once its
  requests in flight are answered or after 5 seconds, the acks they leave are
  written, and the presence of this instance is removed.
  """
  require Logger

  @grace 5_000
  @poll 50
  @ack_grace 2_000

  def draining?, do: :persistent_term.get({__MODULE__, :draining}, false)

  def run do
    Logger.info("draining #{Wrinkle.Socket.count()} connections")
    :persistent_term.put({__MODULE__, :draining}, true)
    Wrinkle.Socket.broadcast({:drain, @grace})
    wait(&sockets_open?/0, System.monotonic_time(:millisecond) + @grace + 2 * @poll)
    wait(&acks_writing?/0, System.monotonic_time(:millisecond) + @ack_grace)
    Wrinkle.Presence.stop()
    Logger.info("drained")
  end

  defp sockets_open?, do: Wrinkle.Socket.count() > 0
  defp acks_writing?, do: Task.Supervisor.children(Wrinkle.AckWrites) != []

  defp wait(busy?, deadline) do
    if busy?.() and System.monotonic_time(:millisecond) < deadline do
      Process.sleep(@poll)
      wait(busy?, deadline)
    end
  end
end
