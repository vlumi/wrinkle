defmodule Wrinkle.SocketTest do
  use ExUnit.Case, async: true

  alias Wrinkle.V1.{Event, Frame, ReadEvent}

  test "event_frame wraps a serialized event without decoding it" do
    for seq <- [1, 200, 70_000] do
      event = %Event{
        cursor: seq,
        body: {:read, %ReadEvent{conversation_id: 7, user_id: 1, seq: seq}}
      }

      payload = Event.encode(event)
      # Pad the text so the frame length crosses varint boundaries.
      frame = Wrinkle.Socket.event_frame(payload)
      assert %Frame{kind: {:event, ^event}} = Frame.decode(frame)
    end
  end

  test "event_frame handles payloads longer than 127 bytes" do
    event = %Event{cursor: 1, body: {:read, %ReadEvent{conversation_id: 7, user_id: 1, seq: 1}}}
    payload = Event.encode(event) <> :binary.copy(<<0>>, 0)

    long = %Event{
      event
      | body:
          {:message_failed, %Wrinkle.V1.MessageFailedEvent{reason: String.duplicate("x", 300)}}
    }

    assert %Frame{kind: {:event, ^long}} =
             Frame.decode(Wrinkle.Socket.event_frame(Event.encode(long)))

    assert %Frame{kind: {:event, ^event}} = Frame.decode(Wrinkle.Socket.event_frame(payload))
  end
end
