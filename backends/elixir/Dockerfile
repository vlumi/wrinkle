# The Elixir gateway, for the containerized cluster. Built from the repository root.
FROM elixir:1.20.4-otp-29
WORKDIR /app
RUN mix local.hex --force && mix local.rebar --force
COPY backends/elixir/mix.exs backends/elixir/mix.lock ./
RUN mix deps.get && mix deps.compile
COPY backends/elixir/config config
COPY backends/elixir/lib lib
RUN mix compile
CMD ["mix", "run", "--no-halt"]
