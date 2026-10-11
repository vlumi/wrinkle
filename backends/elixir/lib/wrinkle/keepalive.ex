defmodule Wrinkle.Keepalive do
  @moduledoc """
  One sweep over the gateway's connections every 30 seconds instead of a
  timer in each: a connection pings its device, or closes if it has received
  nothing for 90 seconds.
  """
  use GenServer

  @interval 30_000
  @idle 90_000

  def start_link(_), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @impl true
  def init(_) do
    schedule()
    {:ok, nil}
  end

  @impl true
  def handle_info(:sweep, state) do
    Wrinkle.Socket.broadcast({:keepalive, System.monotonic_time(:millisecond) - @idle})
    schedule()
    {:noreply, state}
  end

  defp schedule, do: Process.send_after(self(), :sweep, @interval)
end
