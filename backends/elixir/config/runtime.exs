import Config

# The same names as the other backends' flags; a gateway only, so it forwards
# to a services process and needs no database.

# WRINKLE_LISTEN is host:port; an empty host means every interface.
listen = System.get_env("WRINKLE_LISTEN", "127.0.0.1:8080")

{host, port} =
  case Regex.run(~r/^\[?([^\[\]]*)\]?:(\d+)$/, listen) do
    [_, host, port] -> {String.to_charlist(host), String.to_integer(port)}
    nil -> raise "WRINKLE_LISTEN must be host:port, got #{inspect(listen)}"
  end

resolve = fn family ->
  case :inet.getaddr(host, family) do
    {:ok, ip} -> ip
    {:error, _} -> nil
  end
end

ip =
  if host == [],
    do: {0, 0, 0, 0},
    else: resolve.(:inet) || resolve.(:inet6) || raise("WRINKLE_LISTEN: cannot resolve #{host}")

{:ok, hostname} = :inet.gethostname()

config :wrinkle,
  listen: {ip, port},
  services: System.get_env("WRINKLE_SERVICES", "localhost:9000"),
  valkey: System.get_env("WRINKLE_VALKEY", "localhost:6379"),
  node: System.get_env("WRINKLE_NODE", List.to_string(hostname))
