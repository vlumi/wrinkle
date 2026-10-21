defmodule Wrinkle.AckWriter do
  @moduledoc """
  A connection's acks: the highest cursor not yet written is written at most
  once per interval, measured between the starts of writes, and at most one
  write is in flight. A pure function of the time passed in, but for
  `final_write/2`, which runs in the task that writes the last cursor after the
  connection ended.
  """

  # nil until the first ack; then when the last write started, the highest
  # cursor not yet written, and whether a write is in flight or scheduled.
  @type phase :: :idle | :writing | :scheduled | :closed
  @type t :: nil | {started :: integer(), pending :: nil | non_neg_integer(), phase()}
  @type action :: :none | {:write, non_neg_integer()} | {:schedule, delay :: non_neg_integer()}

  @doc "An ack of `cursor` arrived at `now` (milliseconds)."
  @spec ack(t(), non_neg_integer(), integer(), non_neg_integer()) :: {action(), t()}
  def ack(nil, cursor, now, _interval), do: {{:write, cursor}, {now, nil, :writing}}
  def ack({started, nil, :idle}, cursor, now, interval), do: next(started, cursor, now, interval)
  def ack({_, _, :closed} = acks, _cursor, _now, _interval), do: {:none, acks}

  def ack({started, pending, phase}, cursor, _now, _interval),
    do: {:none, {started, highest(pending, cursor), phase}}

  @doc "The write in flight finished, successfully or not."
  @spec done(t(), integer(), non_neg_integer()) :: {action(), t()}
  def done({started, nil, :writing}, _now, _interval), do: {:none, {started, nil, :idle}}
  def done({started, pending, :writing}, now, interval), do: next(started, pending, now, interval)
  def done(acks, _now, _interval), do: {:none, acks}

  @doc "The scheduled write is due."
  @spec due(t(), integer()) :: {action(), t()}
  def due({started, nil, :scheduled}, _now), do: {:none, {started, nil, :idle}}
  def due({_, pending, :scheduled}, now), do: {{:write, pending}, {now, nil, :writing}}
  def due(acks, _now), do: {:none, acks}

  @doc "Forgets the pending cursor, keeping the timing; for a Connect undone."
  @spec drop(t()) :: t()
  def drop(nil), do: nil
  def drop({started, _, phase}), do: {started, nil, phase}

  @doc """
  The connection ended: the cursor to write at once, if any, and whether a
  write is still in flight, to be waited for first. Nothing is scheduled after.
  """
  @spec close(t()) :: {nil | non_neg_integer(), boolean(), t()}
  def close(nil), do: {nil, false, nil}
  def close({started, pending, phase}), do: {pending, phase == :writing, {started, nil, :closed}}

  @doc "Waits for the write in flight, if any, to end, then writes."
  @spec final_write(pid() | nil, (-> any())) :: any()
  def final_write(nil, write), do: write.()

  def final_write(in_flight, write) do
    ref = Process.monitor(in_flight)

    receive do
      {:DOWN, ^ref, :process, _, _} -> write.()
    end
  end

  defp next(started, cursor, now, interval) do
    if now - started >= interval,
      do: {{:write, cursor}, {now, nil, :writing}},
      else: {{:schedule, started + interval - now}, {started, cursor, :scheduled}}
  end

  defp highest(nil, cursor), do: cursor
  defp highest(pending, cursor), do: max(pending, cursor)
end
