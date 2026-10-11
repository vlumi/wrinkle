defmodule Wrinkle.Metrics do
  @moduledoc """
  The metrics every backend exposes (docs/metrics.md), in the Prometheus text
  format, from ETS counters. Resident memory is not available from the VM;
  the load generator reads it with ps.
  """

  @table __MODULE__
  @buckets Enum.map(0..15, fn i -> 0.0005 * :math.pow(2, i) end)

  def init do
    :ets.new(@table, [:named_table, :public, :set, write_concurrency: true])
    :ets.insert(@table, {:connections, 0})
    :ets.insert(@table, {:pushed, 0})
    :ok
  end

  def connection(delta), do: :ets.update_counter(@table, :connections, delta)
  def pushed, do: :ets.update_counter(@table, :pushed, 1)

  def request(type, code, native_duration) do
    nanoseconds = System.convert_time_unit(native_duration, :native, :nanosecond)
    seconds = nanoseconds / 1.0e9
    increment({:requests, type, code}, 1)
    increment({:duration_count, type}, 1)
    increment({:duration_sum_ns, type}, nanoseconds)
    for le <- @buckets, seconds <= le, do: increment({:bucket, type, le}, 1)
    :ok
  end

  defp increment(key, by), do: :ets.update_counter(@table, key, by, {key, 0})

  def render do
    rows = :ets.tab2list(@table)
    values = Map.new(rows)
    get = &Map.get(values, &1, 0)
    {runtime_ms, _} = :erlang.statistics(:runtime)

    requests =
      for {{:requests, type, code}, n} <- rows do
        ~s(wrinkle_requests_total{type="#{type}",code="#{code}"} #{n})
      end

    histograms =
      for {{:duration_count, type}, count} <- rows, line <- histogram(type, count, get), do: line

    lines =
      [
        "# TYPE wrinkle_connections gauge",
        "wrinkle_connections #{get.(:connections)}",
        "# TYPE wrinkle_events_pushed_total counter",
        "wrinkle_events_pushed_total #{get.(:pushed)}",
        "# TYPE wrinkle_requests_total counter"
      ] ++
        requests ++
        ["# TYPE wrinkle_request_duration_seconds histogram"] ++
        histograms ++
        [
          "# TYPE process_cpu_seconds_total counter",
          "process_cpu_seconds_total #{runtime_ms / 1000}",
          "# TYPE erlang_vm_memory_bytes_total gauge",
          ~s(erlang_vm_memory_bytes_total{kind="processes"} #{:erlang.memory(:processes)}),
          ~s(erlang_vm_memory_bytes_total{kind="system"} #{:erlang.memory(:system)})
        ]

    Enum.join(lines, "\n") <> "\n"
  end

  defp histogram(type, count, get) do
    name = "wrinkle_request_duration_seconds"

    buckets =
      for le <- @buckets do
        ~s(#{name}_bucket{type="#{type}",le="#{le}"} #{get.({:bucket, type, le})})
      end

    buckets ++
      [
        ~s(#{name}_bucket{type="#{type}",le="+Inf"} #{count}),
        ~s(#{name}_count{type="#{type}"} #{count}),
        ~s(#{name}_sum{type="#{type}"} #{get.({:duration_sum_ns, type}) / 1.0e9})
      ]
  end
end
