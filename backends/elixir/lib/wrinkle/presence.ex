defmodule Wrinkle.Presence do
  @moduledoc """
  Which node holds each device's connection: one Valkey hash per user, device
  to node. The services read it to notify the devices that are not connected.
  """

  def put(user_id, device_id) do
    node = Application.fetch_env!(:wrinkle, :node)

    Redix.noreply_command(Wrinkle.Valkey, [
      "HSET",
      key(user_id),
      Integer.to_string(device_id),
      node
    ])
  end

  def delete(user_id, device_id) do
    Redix.noreply_command(Wrinkle.Valkey, ["HDEL", key(user_id), Integer.to_string(device_id)])
  end

  defp key(user_id), do: "presence:#{user_id}"
end
