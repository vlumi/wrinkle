defmodule Wrinkle.PushTest do
  use ExUnit.Case, async: true

  alias Wrinkle.V1.{Event, Frame, MessageFailedEvent, ReadEvent}

  defp wrapped(event), do: Frame.decode(Wrinkle.Push.event_frame(Event.encode(event)))

  test "event_frame wraps a serialized event without decoding it" do
    event = %Event{cursor: 1, body: {:read, %ReadEvent{conversation_id: 7, user_id: 1, seq: 1}}}
    assert %Frame{kind: {:event, ^event}} = wrapped(event)
  end

  test "event_frame encodes lengths across varint boundaries" do
    for len <- [1, 200, 70_000] do
      event = %Event{
        cursor: len,
        body:
          {:message_failed,
           %MessageFailedEvent{conversation_id: 7, client_message_id: String.duplicate("x", len)}}
      }

      assert %Frame{kind: {:event, ^event}} = wrapped(event)
    end
  end
end
