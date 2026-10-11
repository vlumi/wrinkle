# The Elixir gateway, for the containerized cluster. Built from the repository root.
# In the prod environment the application is permanent: if it fails, the VM
# exits and the container ends.
FROM elixir:1.20.4-otp-29
ENV MIX_ENV=prod
WORKDIR /app
RUN mix local.hex --force && mix local.rebar --force
COPY backends/elixir/mix.exs backends/elixir/mix.lock ./
RUN mix deps.get --only prod && mix deps.compile
COPY backends/elixir/config config
COPY backends/elixir/lib lib
RUN mix compile
CMD ["mix", "run", "--no-halt"]
