defmodule Wrinkle.TokenBucketTest do
  use ExUnit.Case, async: true

  alias Wrinkle.TokenBucket

  defp take_all(bucket, now, n) do
    Enum.map_reduce(1..n, bucket, fn _, b -> TokenBucket.take(b, now) end)
  end

  test "a burst of 100 passes, the next frame does not" do
    {results, bucket} = take_all(TokenBucket.new(0), 0, 100)
    assert Enum.all?(results, &(&1 == :ok))
    assert {:empty, _} = TokenBucket.take(bucket, 0)
  end

  test "refills 50 tokens a second" do
    {_, bucket} = take_all(TokenBucket.new(0), 0, 100)
    assert {:empty, bucket} = TokenBucket.take(bucket, 19)
    assert {:ok, bucket} = TokenBucket.take(bucket, 20)
    assert {:empty, _} = TokenBucket.take(bucket, 20)

    {results, _} = take_all(bucket, 1_020, 51)
    assert Enum.frequencies(results) == %{ok: 50, empty: 1}
  end

  test "does not refill beyond the capacity" do
    {_, bucket} = take_all(TokenBucket.new(0), 0, 100)
    {results, _} = take_all(bucket, 60_000, 101)
    assert Enum.frequencies(results) == %{ok: 100, empty: 1}
  end

  test "an empty take does not lose the partial refill" do
    {_, bucket} = take_all(TokenBucket.new(0), 0, 100)
    {:empty, bucket} = TokenBucket.take(bucket, 10)
    assert {:ok, _} = TokenBucket.take(bucket, 20)
  end
end
