defmodule Wrinkle.AckWriterTest do
  use ExUnit.Case, async: true

  alias Wrinkle.AckWriter

  @interval 1_000

  defp ack(acks, cursor, now, interval \\ @interval),
    do: AckWriter.ack(acks, cursor, now, interval)

  test "the first ack is written at once" do
    assert {{:write, 5}, {0, nil, :writing}} = ack(nil, 5, 0)
  end

  test "an ack after the interval is written at once" do
    {_, acks} = ack(nil, 5, 0)
    {:none, acks} = AckWriter.done(acks, 10, @interval)
    assert {{:write, 6}, _} = ack(acks, 6, 1_000)
  end

  test "acks within the interval are held and written once, the highest, after it" do
    {_, acks} = ack(nil, 5, 0)
    {:none, acks} = AckWriter.done(acks, 10, @interval)
    assert {{:schedule, 700}, acks} = ack(acks, 9, 300)
    {:none, acks} = ack(acks, 7, 400)
    {:none, acks} = ack(acks, 12, 900)
    assert {{:write, 12}, {1_000, nil, :writing}} = AckWriter.due(acks, 1_000)
  end

  test "acks during a write are scheduled from the write's start, not at once" do
    {_, acks} = ack(nil, 5, 0)
    {:none, acks} = ack(acks, 9, 100)
    {:none, acks} = ack(acks, 7, 200)
    assert {{:schedule, 700}, acks} = AckWriter.done(acks, 300, @interval)
    assert {{:write, 9}, _} = AckWriter.due(acks, 1_000)
  end

  test "a write that took longer than the interval is followed at once" do
    {_, acks} = ack(nil, 5, 0)
    {:none, acks} = ack(acks, 9, 100)
    assert {{:write, 9}, {1_500, nil, :writing}} = AckWriter.done(acks, 1_500, @interval)
  end

  test "goes idle when nothing is pending" do
    {_, acks} = ack(nil, 5, 0)
    assert {:none, {0, nil, :idle}} = AckWriter.done(acks, 10, @interval)
  end

  test "at most one write is in flight however many acks arrive" do
    {first, acks} = ack(nil, 1, 0)

    {actions, acks} =
      Enum.map_reduce(2..1000, acks, fn cursor, acks -> ack(acks, cursor, cursor) end)

    assert first == {:write, 1}
    assert Enum.all?(actions, &(&1 == :none))
    assert {{:write, 1000}, acks} = AckWriter.done(acks, 2_000, @interval)
    assert {:none, _} = ack(acks, 1001, 2_001)
    assert {:none, _} = AckWriter.due(acks, 2_001)
  end

  test "ending the connection flushes the pending cursor and cancels the scheduled write" do
    {_, acks} = ack(nil, 5, 0)
    {:none, acks} = AckWriter.done(acks, 10, @interval)
    {{:schedule, _}, acks} = ack(acks, 9, 300)
    assert {9, false, closed} = AckWriter.close(acks)
    assert {:none, _} = AckWriter.due(closed, 1_000)
    assert {:none, _} = ack(closed, 10, 1_000)
  end

  test "ending the connection during a write flushes after it" do
    {_, acks} = ack(nil, 5, 0)
    {:none, acks} = ack(acks, 9, 100)
    assert {9, true, _} = AckWriter.close(acks)
  end

  test "ending the connection with nothing pending writes nothing" do
    assert {nil, false, _} = AckWriter.close(nil)
    {_, acks} = ack(nil, 5, 0)
    assert {nil, true, _} = AckWriter.close(acks)
  end

  test "a dropped cursor is not written when its schedule comes due" do
    {_, acks} = ack(nil, 5, 0)
    {:none, acks} = AckWriter.done(acks, 10, @interval)
    {{:schedule, _}, acks} = ack(acks, 9, 300)
    assert {:none, {0, nil, :idle}} = AckWriter.due(AckWriter.drop(acks), 1_000)
  end

  test "interval 0 writes every ack as it comes, one at a time" do
    assert {{:write, 5}, acks} = ack(nil, 5, 0, 0)
    {:none, acks} = ack(acks, 9, 0, 0)
    {:none, acks} = ack(acks, 7, 0, 0)
    assert {{:write, 9}, acks} = AckWriter.done(acks, 0, 0)
    assert {:none, acks} = AckWriter.done(acks, 0, 0)
    assert {{:write, 10}, _} = ack(acks, 10, 0, 0)
  end

  test "a cursor of 0 is written" do
    {_, acks} = ack(nil, 5, 0, 0)
    {:none, acks} = ack(acks, 0, 0, 0)
    assert {{:write, 0}, _} = AckWriter.done(acks, 0, 0)
  end

  test "the final write waits for the write in flight" do
    parent = self()
    in_flight = spawn(fn -> receive(do: (:finish -> send(parent, {:wrote, 5}))) end)
    final = spawn(fn -> AckWriter.final_write(in_flight, fn -> send(parent, {:wrote, 9}) end) end)
    ref = Process.monitor(final)

    refute_receive {:wrote, _}, 50
    send(in_flight, :finish)
    assert_receive {:wrote, 5}
    assert_receive {:wrote, 9}
    assert_receive {:DOWN, ^ref, :process, _, :normal}
  end

  test "the final write without a write in flight is at once" do
    assert :ok = AckWriter.final_write(nil, fn -> :ok end)
  end
end
