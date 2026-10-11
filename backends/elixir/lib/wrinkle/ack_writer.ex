defmodule Wrinkle.AckWriter do
  @moduledoc """
  A connection's acks: at most one write in flight; acks arriving meanwhile
  replace the pending cursor, keeping the highest, which is written when the
  current write finishes.
  """

  # nil when idle; {:writing, next} while a write is in flight, next being
  # the cursor to write after it, if any.
  @type t :: nil | {:writing, nil | non_neg_integer()}
  @type action :: :none | {:write, non_neg_integer()}

  @spec ack(t(), non_neg_integer()) :: {action(), t()}
  def ack(nil, cursor), do: {{:write, cursor}, {:writing, nil}}
  def ack({:writing, nil}, cursor), do: {:none, {:writing, cursor}}
  def ack({:writing, next}, cursor), do: {:none, {:writing, max(next, cursor)}}

  @doc "The write in flight finished, successfully or not."
  @spec done(t()) :: {action(), t()}
  def done({:writing, nil}), do: {:none, nil}
  def done({:writing, next}), do: {{:write, next}, {:writing, nil}}
end
