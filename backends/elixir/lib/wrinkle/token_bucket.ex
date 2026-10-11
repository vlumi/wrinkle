defmodule Wrinkle.TokenBucket do
  @moduledoc """
  A connection's rate limit: 100 tokens, refilled at 50 a second, one per
  inbound frame. Kept in thousandths of a token, so the arithmetic stays in
  small integers.
  """

  @scale 1000
  @capacity 100 * @scale
  # 50 tokens a second is 50 thousandths a millisecond.
  @refill_per_ms 50

  @type t :: {non_neg_integer(), integer()}

  @spec new(integer()) :: t()
  def new(now_ms), do: {@capacity, now_ms}

  @doc "Takes a token at `now_ms` (monotonic milliseconds)."
  @spec take(t(), integer()) :: {:ok | :empty, t()}
  def take({tokens, at}, now_ms) do
    tokens = min(@capacity, tokens + max(now_ms - at, 0) * @refill_per_ms)

    if tokens >= @scale do
      {:ok, {tokens - @scale, now_ms}}
    else
      {:empty, {tokens, now_ms}}
    end
  end
end
