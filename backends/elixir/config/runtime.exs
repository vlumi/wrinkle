import Config

# The same names as the other backends' flags; a gateway only, so it forwards
# to a services process and needs no database.
port =
  System.get_env("WRINKLE_LISTEN", ":8080")
  |> String.split(":")
  |> List.last()
  |> String.to_integer()

config :wrinkle,
  port: port,
  services: System.get_env("WRINKLE_SERVICES", "localhost:9000"),
  valkey: System.get_env("WRINKLE_VALKEY", "localhost:6379"),
  node: System.get_env("WRINKLE_NODE", elem(:inet.gethostname(), 1) |> List.to_string())
