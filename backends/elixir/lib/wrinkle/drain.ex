defmodule Wrinkle.Drain do
  @moduledoc """
  Graceful shutdown, run as the application stops (SIGTERM stops it): health
  checks and upgrades get 503, every connection closes with 1001 once its
  requests in flight are answered or after 5 seconds, and the presence of
  this instance is removed.
  """
  require Logger

  @grace 5_000
  @poll 50

  def draining?, do: :persistent_term.get({__MODULE__, :draining}, false)

  def run do
    Logger.info("draining #{Wrinkle.Socket.count()} connections")
    :persistent_term.put({__MODULE__, :draining}, true)
    Wrinkle.Socket.broadcast({:drain, @grace})
    wait_closed(System.monotonic_time(:millisecond) + @grace + 2 * @poll)
    Wrinkle.Presence.stop()
    Logger.info("drained")
  end

  defp wait_closed(deadline) do
    if Wrinkle.Socket.count() > 0 and System.monotonic_time(:millisecond) < deadline do
      Process.sleep(@poll)
      wait_closed(deadline)
    end
  end
end
