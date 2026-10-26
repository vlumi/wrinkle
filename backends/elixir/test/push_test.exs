defmodule Wrinkle.PushTest do
  use ExUnit.Case, async: true
  import Bitwise

  alias Wrinkle.Backend.V1.{Push, SignedOut}
  alias Wrinkle.V1.{Event, Frame, Message, MessageEvent, ReadEvent}

  defp wrapped(event), do: Frame.decode(Wrinkle.Push.event_frame(Event.encode(event)))

  test "event_frame wraps a serialized event without decoding it" do
    id = :binary.copy(<<7>>, 16)
    event = %Event{cursor: 1, body: {:read, %ReadEvent{conversation_id: id, user_id: id, seq: 1}}}
    assert %Frame{kind: {:event, ^event}} = wrapped(event)
  end

  test "event_frame encodes lengths across varint boundaries" do
    for len <- [1, 200, 70_000] do
      event = %Event{
        cursor: len,
        body:
          {:message,
           %MessageEvent{
             message: %Message{
               conversation_id: :binary.copy(<<7>>, 16),
               client_message_id: String.duplicate("x", len)
             }
           }}
      }

      assert %Frame{kind: {:event, ^event}} = wrapped(event)
    end
  end

  test "hex names a user by the lowercase hex of the 16-byte id" do
    id = <<0xFF, 0::size(14)-unit(8), 0x01>>
    assert Wrinkle.Push.hex(id) == "ff000000000000000000000000000001"
  end

  defp read_event do
    id = :binary.copy(<<7>>, 16)
    %Event{cursor: 1, body: {:read, %ReadEvent{conversation_id: id, user_id: id, seq: 1}}}
  end

  defp decoded_event(payload) do
    assert {:event, frame} = Wrinkle.Push.decode(payload)
    Frame.decode(frame)
  end

  test "decode wraps the event of a push without decoding it" do
    for len <- [1, 200, 70_000] do
      event = %Event{
        cursor: len,
        body:
          {:message,
           %MessageEvent{
             message: %Message{
               conversation_id: :binary.copy(<<7>>, 16),
               client_message_id: String.duplicate("x", len)
             }
           }}
      }

      push = Push.encode(%Push{kind: {:event, Event.encode(event)}})
      assert %Frame{kind: {:event, ^event}} = decoded_event(push)
    end
  end

  test "decode falls back to a full decode, the last field of the oneof winning" do
    event = read_event()

    payload =
      Push.encode(%Push{kind: {:signed_out, %SignedOut{device_ids: [1]}}}) <>
        Push.encode(%Push{kind: {:event, Event.encode(event)}})

    assert %Frame{kind: {:event, ^event}} = decoded_event(payload)
  end

  test "decode reads sign-outs" do
    ids = [3, 1 <<< 40]
    push = Push.encode(%Push{kind: {:signed_out, %SignedOut{device_ids: ids}}})
    assert Wrinkle.Push.decode(push) == {:signed_out, ids}

    push = Push.encode(%Push{kind: {:signed_out, %SignedOut{all_devices: true}}})
    assert Wrinkle.Push.decode(push) == {:signed_out, :all}
  end

  test "decode refuses what is not a push" do
    assert Wrinkle.Push.decode(<<0x0A, 0x05, 1>>) == :error
    assert Wrinkle.Push.decode(<<0xFF>>) == :error
    assert Wrinkle.Push.decode(<<>>) == :error
  end
end
