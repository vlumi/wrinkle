defmodule Wrinkle.AckWriterTest do
  use ExUnit.Case, async: true

  alias Wrinkle.AckWriter

  test "an ack on an idle connection is written at once" do
    assert {{:write, 5}, {:writing, nil}} = AckWriter.ack(nil, 5)
  end

  test "acks during a write are coalesced into the highest" do
    {_, acks} = AckWriter.ack(nil, 5)
    {:none, acks} = AckWriter.ack(acks, 9)
    {:none, acks} = AckWriter.ack(acks, 7)
    assert {{:write, 9}, {:writing, nil}} = AckWriter.done(acks)
  end

  test "goes idle when nothing is pending" do
    {_, acks} = AckWriter.ack(nil, 5)
    assert {:none, nil} = AckWriter.done(acks)
  end

  test "a cursor of 0 is written after the current write" do
    {_, acks} = AckWriter.ack(nil, 5)
    {:none, acks} = AckWriter.ack(acks, 0)
    assert {{:write, 0}, _} = AckWriter.done(acks)
  end

  test "at most one write is in flight however many acks arrive" do
    {first, acks} = AckWriter.ack(nil, 1)

    {actions, acks} =
      Enum.map_reduce(2..1000, acks, fn cursor, acks -> AckWriter.ack(acks, cursor) end)

    assert first == {:write, 1}
    assert Enum.all?(actions, &(&1 == :none))
    assert {{:write, 1000}, acks} = AckWriter.done(acks)
    assert {:none, nil} = AckWriter.done(acks)
  end
end
