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
    seconds = System.convert_time_unit(native_duration, :native, :nanosecond) / 1.0e9
    :ets.update_counter(@table, {:requests, type, code}, 1, {{:requests, type, code}, 0})
    :ets.update_counter(@table, {:duration_count, type}, 1, {{:duration_count, type}, 0})

    :ets.update_counter(
      @table,
      {:duration_sum_ns, type},
      round(seconds * 1.0e9),
      {{:duration_sum_ns, type}, 0}
    )

    for le <- @buckets, seconds <= le do
      :ets.update_counter(@table, {:bucket, type, le}, 1, {{:bucket, type, le}, 0})
    end

    :ok
  end

  def render do
    rows = :ets.tab2list(@table)
    get = fn key -> Enum.find_value(rows, 0, fn {k, v} -> if k == key, do: v end) end
    {runtime_ms, _} = :erlang.statistics(:runtime)

    requests =
      for {{:requests, type, code}, n} <- rows do
        ~s(wrinkle_requests_total{type="#{type}",code="#{code}"} #{n})
      end

    types =
      rows
      |> Enum.flat_map(fn
        {{:duration_count, t}, _} -> [t]
        _ -> []
      end)
      |> Enum.uniq()

    histograms =
      Enum.flat_map(types, fn type ->
        buckets =
          Enum.map(@buckets, fn le ->
            ~s(wrinkle_request_duration_seconds_bucket{type="#{type}",le="#{le}"} #{get.({:bucket, type, le})})
          end)

        buckets ++
          [
            ~s(wrinkle_request_duration_seconds_bucket{type="#{type}",le="+Inf"} #{get.({:duration_count, type})}),
            ~s(wrinkle_request_duration_seconds_count{type="#{type}"} #{get.({:duration_count, type})}),
            ~s(wrinkle_request_duration_seconds_sum{type="#{type}"} #{get.({:duration_sum_ns, type}) / 1.0e9})
          ]
      end)

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
end
