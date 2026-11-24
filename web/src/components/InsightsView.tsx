import { useEffect, useMemo, useState } from 'react';
import {
  ResponsiveContainer,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  CartesianGrid,
} from 'recharts';
import { RefreshCw, ArrowUpDown, Calendar, AlertCircle } from 'lucide-react';
import type { ArchiveClient, InsightsResponse, ModelInsight } from '../lib/api';
import {
  PROVIDER_META,
  formatCompact,
  formatLatencySeconds,
  formatNumber,
  formatPercent,
  formatShortDay,
  inferProvider,
  type Provider,
} from '../lib/format';

interface InsightsViewProps {
  client: ArchiveClient;
  onSelectModel: (model: string) => void;
}

type WindowPreset = '7' | '30' | '90' | 'custom';
type SortKey =
  | 'requests'
  | 'sessions'
  | 'turns'
  | 'steps_per_turn'
  | 'avg_latency_seconds'
  | 'failure_rate'
  | 'cancel_rate';

interface MetricSpec {
  id: string;
  title: string;
  subtitle: string;
  getValue: (m: ModelInsight) => number;
  format: (v: number) => string;
  filter?: (m: ModelInsight) => boolean;
}

const METRICS: MetricSpec[] = [
  {
    id: 'requests',
    title: 'Requests',
    subtitle: 'Total model calls',
    getValue: (m) => m.requests,
    format: (v) => formatCompact(v),
  },
  {
    id: 'sessions',
    title: 'Sessions',
    subtitle: 'Distinct conversations',
    getValue: (m) => m.sessions,
    format: (v) => formatCompact(v),
  },
  {
    id: 'steps_per_turn',
    title: 'Steps / Turn',
    subtitle: 'Requests per user turn',
    getValue: (m) => m.steps_per_turn,
    format: (v) => v.toFixed(1),
    filter: (m) => m.turns > 0 && m.steps_per_turn > 0,
  },
  {
    id: 'avg_latency',
    title: 'Avg Latency',
    subtitle: 'Succeeded request duration',
    getValue: (m) => m.avg_latency_seconds,
    format: (v) => formatLatencySeconds(v),
    filter: (m) => m.avg_latency_seconds > 0,
  },
  {
    id: 'failure_rate',
    title: 'Failure Rate',
    subtitle: 'Failed or rejected calls',
    getValue: (m) => m.failure_rate,
    format: (v) => formatPercent(v),
  },
  {
    id: 'cancel_rate',
    title: 'Cancel Rate',
    subtitle: 'Client-canceled calls',
    getValue: (m) => m.cancel_rate,
    format: (v) => formatPercent(v),
  },
];

function defaultCustomDates() {
  const now = new Date();
  const to = now.toISOString().slice(0, 10);
  const fromDate = new Date(now.getTime() - 29 * 86400 * 1000);
  const from = fromDate.toISOString().slice(0, 10);
  return { from, to };
}

export function InsightsView({ client, onSelectModel }: InsightsViewProps) {
  const [preset, setPreset] = useState<WindowPreset>('30');
  const [{ from, to }, setCustomDates] = useState(defaultCustomDates);
  const [appliedRange, setAppliedRange] = useState<{ from: string; to: string } | null>(null);
  const [data, setData] = useState<InsightsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [reloadTick, setReloadTick] = useState(0);
  const [sortKey, setSortKey] = useState<SortKey>('requests');
  const [sortDesc, setSortDesc] = useState(true);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);

    const params =
      preset === 'custom' && appliedRange
        ? { from: appliedRange.from, to: appliedRange.to }
        : { days: Number(preset === 'custom' ? '30' : preset) };

    client
      .getInsights(params, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) {
          setData(res);
          setLoading(false);
        }
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        setError(err instanceof Error ? err.message : 'Failed to load fleet insights');
        setLoading(false);
      });

    return () => controller.abort();
  }, [client, preset, appliedRange, reloadTick]);

  const dailyRows = useMemo(() => {
    return (data?.daily ?? []).map((d) => ({
      day: d.day,
      label: formatShortDay(d.day),
      anthropic: d.requests.anthropic ?? 0,
      openai: d.requests.openai ?? 0,
      google: d.requests.google ?? 0,
      other: d.requests.other ?? 0,
      total:
        (d.requests.anthropic ?? 0) +
        (d.requests.openai ?? 0) +
        (d.requests.google ?? 0) +
        (d.requests.other ?? 0),
    }));
  }, [data]);

  const sortedModels = useMemo(() => {
    const list = [...(data?.models ?? [])];
    list.sort((a, b) => {
      const diff = (a[sortKey] ?? 0) - (b[sortKey] ?? 0);
      return sortDesc ? -diff : diff;
    });
    return list;
  }, [data, sortKey, sortDesc]);

  const toggleSort = (key: SortKey) => {
    if (sortKey === key) {
      setSortDesc((d) => !d);
    } else {
      setSortKey(key);
      setSortDesc(true);
    }
  };

  const applyCustomRange = () => {
    if (from && to && from <= to) {
      setAppliedRange({ from, to });
    }
  };

  return (
    <main className="mx-auto max-w-[1440px] px-4 sm:px-6 py-6 sm:py-8 space-y-8">
      {/* Header & Window Controls */}
      <div className="flex flex-col sm:flex-row sm:items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-white">Fleet Insights</h1>
          <p className="mt-1 text-sm text-zinc-400">
            {data
              ? `${data.window_days} days (${data.from.slice(0, 10)} → ${data.to.slice(0, 10)} UTC)`
              : 'Model behavior, volume, and reliability across ClickHouse.'}
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <div className="inline-flex items-center rounded-lg border border-zinc-800 bg-zinc-950 p-1">
            {(['7', '30', '90', 'custom'] as WindowPreset[]).map((p) => (
              <button
                key={p}
                type="button"
                onClick={() => {
                  setPreset(p);
                  if (p === 'custom' && !appliedRange) {
                    setAppliedRange({ from, to });
                  }
                }}
                className={`rounded-md px-3 py-1 text-xs font-medium transition-colors cursor-pointer ${
                  preset === p
                    ? 'bg-zinc-800 text-white'
                    : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900'
                }`}
              >
                {p === 'custom' ? 'Custom' : `${p}d`}
              </button>
            ))}
          </div>

          <button
            type="button"
            onClick={() => setReloadTick((t) => t + 1)}
            disabled={loading}
            title="Refresh"
            className="inline-flex items-center justify-center rounded-lg border border-zinc-800 bg-zinc-950 p-2 text-zinc-400 hover:text-white hover:border-zinc-700 disabled:opacity-50 transition-colors cursor-pointer"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
          </button>
        </div>
      </div>

      {/* Custom Date Range Bar */}
      {preset === 'custom' && (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border border-zinc-800/90 bg-[#0a0a0a] px-4 py-3 text-xs">
          <Calendar className="w-4 h-4 text-zinc-500" />
          <label className="flex items-center gap-2 text-zinc-400">
            <span>From (UTC)</span>
            <input
              type="date"
              value={from}
              onChange={(e) => setCustomDates((prev) => ({ ...prev, from: e.target.value }))}
              className="rounded-md border border-zinc-800 bg-black px-2.5 py-1 text-zinc-100 focus:border-zinc-500 focus:outline-none"
            />
          </label>
          <label className="flex items-center gap-2 text-zinc-400">
            <span>Through (UTC)</span>
            <input
              type="date"
              value={to}
              onChange={(e) => setCustomDates((prev) => ({ ...prev, to: e.target.value }))}
              className="rounded-md border border-zinc-800 bg-black px-2.5 py-1 text-zinc-100 focus:border-zinc-500 focus:outline-none"
            />
          </label>
          <button
            type="button"
            onClick={applyCustomRange}
            className="rounded-md bg-zinc-100 px-3 py-1 font-medium text-black hover:bg-white transition-colors cursor-pointer"
          >
            Apply
          </button>
        </div>
      )}

      {error && (
        <div
          role="alert"
          className="flex items-center gap-2.5 rounded-xl border border-red-500/30 bg-red-950/30 px-4 py-3 text-sm text-red-300"
        >
          <AlertCircle className="w-4 h-4 shrink-0 text-red-400" />
          <span>{error}</span>
        </div>
      )}

      {/* KPI Strip */}
      <section className="grid grid-cols-2 lg:grid-cols-4 gap-3 sm:gap-4">
        {[
          { label: 'Requests', value: data ? formatNumber(data.totals.requests) : '—' },
          { label: 'Sessions', value: data ? formatNumber(data.totals.sessions) : '—' },
          { label: 'Turns', value: data ? formatNumber(data.totals.turns) : '—' },
          { label: 'Active Models', value: data ? formatNumber(data.totals.models) : '—' },
        ].map((kpi) => (
          <div
            key={kpi.label}
            className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] px-5 py-4"
          >
            <div className="text-xs font-medium uppercase tracking-wider text-zinc-500">
              {kpi.label}
            </div>
            <div className="mt-2 text-3xl font-semibold tracking-tight text-white">
              {loading && !data ? (
                <span className="inline-block h-8 w-24 rounded bg-zinc-900 animate-pulse" />
              ) : (
                kpi.value
              )}
            </div>
          </div>
        ))}
      </section>

      {/* Daily Activity Stacked Bar Chart */}
      <section className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] p-5">
        <div className="flex flex-wrap items-center justify-between gap-3 mb-5">
          <div>
            <h2 className="text-sm font-semibold text-white">Daily Requests by Provider</h2>
            <p className="text-xs text-zinc-500 mt-0.5">UTC daily volume stacked by model family</p>
          </div>
          <div className="flex flex-wrap items-center gap-4 text-xs text-zinc-400">
            {(['anthropic', 'openai', 'google', 'other'] as Provider[]).map((p) => (
              <span key={p} className="inline-flex items-center gap-1.5">
                <span
                  className="w-2.5 h-2.5 rounded-xs"
                  style={{ backgroundColor: PROVIDER_META[p].color }}
                />
                <span>{PROVIDER_META[p].label}</span>
              </span>
            ))}
          </div>
        </div>

        <div className="h-64 w-full">
          {loading && !data ? (
            <div className="h-full w-full rounded-lg bg-zinc-950 animate-pulse" />
          ) : dailyRows.length === 0 ? (
            <div className="h-full flex items-center justify-center text-sm text-zinc-500">
              No request activity in this window.
            </div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={dailyRows} margin={{ top: 4, right: 4, left: -16, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="#1c1c20" vertical={false} />
                <XAxis
                  dataKey="label"
                  tick={{ fill: '#71717a', fontSize: 11 }}
                  axisLine={{ stroke: '#27272a' }}
                  tickLine={false}
                  minTickGap={24}
                />
                <YAxis
                  tick={{ fill: '#71717a', fontSize: 11 }}
                  axisLine={false}
                  tickLine={false}
                  tickFormatter={(v: number) => formatCompact(v)}
                />
                <Tooltip
                  cursor={{ fill: 'rgba(255,255,255,0.04)' }}
                  contentStyle={{
                    backgroundColor: '#09090b',
                    border: '1px solid #27272a',
                    borderRadius: '8px',
                    fontSize: '12px',
                    color: '#ededed',
                  }}
                  labelStyle={{ color: '#a1a1aa', marginBottom: '4px', fontWeight: 500 }}
                  formatter={(value, name) => {
                    const key = String(name) as Provider;
                    return [formatNumber(Number(value)), PROVIDER_META[key]?.label ?? key];
                  }}
                />
                <Bar dataKey="anthropic" stackId="a" fill="#d97757" radius={[0, 0, 0, 0]} />
                <Bar dataKey="openai" stackId="a" fill="#ededed" radius={[0, 0, 0, 0]} />
                <Bar dataKey="google" stackId="a" fill="#60a5fa" radius={[0, 0, 0, 0]} />
                <Bar dataKey="other" stackId="a" fill="#71717a" radius={[2, 2, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          )}
        </div>
      </section>

      {/* Fleethouse 6-Chart Small Multiples Grid */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between">
          <div>
            <h2 className="text-sm font-semibold text-white">Model Comparison</h2>
            <p className="text-xs text-zinc-500 mt-0.5">
              Click any model to inspect its sessions
            </p>
          </div>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {METRICS.map((spec) => {
            const rows = (data?.models ?? [])
              .filter((m) => (spec.filter ? spec.filter(m) : true))
              .sort((a, b) => spec.getValue(b) - spec.getValue(a))
              .slice(0, 8);
            const maxVal = Math.max(...rows.map((r) => spec.getValue(r)), 0.0001);

            return (
              <div
                key={spec.id}
                data-metric={spec.id}
                className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] p-4 flex flex-col"
              >
                <div className="flex items-baseline justify-between mb-3">
                  <h3 className="text-sm font-medium text-white">{spec.title}</h3>
                  <span className="text-[11px] text-zinc-500">{spec.subtitle}</span>
                </div>

                {loading && !data ? (
                  <div className="space-y-2.5 py-2">
                    {[1, 2, 3, 4].map((n) => (
                      <div key={n} className="h-5 rounded bg-zinc-900 animate-pulse" />
                    ))}
                  </div>
                ) : rows.length === 0 ? (
                  <div className="py-8 text-center text-xs text-zinc-600">No data</div>
                ) : (
                  <div className="space-y-2">
                    {rows.map((m) => {
                      const val = spec.getValue(m);
                      const pct = Math.max(2, (val / maxVal) * 100);
                      const prov = m.provider || inferProvider(m.model);
                      const meta = PROVIDER_META[prov] ?? PROVIDER_META.other;
                      return (
                        <button
                          key={m.model}
                          type="button"
                          data-model={m.model}
                          onClick={() => onSelectModel(m.model)}
                          className="w-full text-left group rounded-md px-1.5 py-1 -mx-1.5 hover:bg-zinc-900/90 transition-colors cursor-pointer"
                        >
                          <div className="flex items-center justify-between gap-2 text-xs mb-1">
                            <span className="font-mono text-zinc-200 group-hover:text-white truncate">
                              {m.model}
                            </span>
                            <span className="font-mono text-zinc-400 group-hover:text-zinc-200 shrink-0">
                              {spec.format(val)}
                            </span>
                          </div>
                          <div className="h-1.5 w-full rounded-full bg-zinc-900 overflow-hidden">
                            <div
                              className="h-full rounded-full transition-all duration-300"
                              style={{
                                width: `${pct}%`,
                                backgroundColor: meta.color,
                              }}
                            />
                          </div>
                        </button>
                      );
                    })}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </section>

      {/* Model Breakdown Table */}
      {sortedModels.length > 0 && (
        <section className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] overflow-hidden">
          <div className="px-5 py-4 border-b border-zinc-800/80 flex items-center justify-between">
            <h2 className="text-sm font-semibold text-white">All Models ({sortedModels.length})</h2>
            <span className="text-xs text-zinc-500">Click column headers to sort</span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs border-collapse">
              <thead>
                <tr className="border-b border-zinc-800/80 text-zinc-500">
                  <th className="py-2.5 px-5 font-medium">Model</th>
                  {(
                    [
                      ['requests', 'Requests'],
                      ['sessions', 'Sessions'],
                      ['turns', 'Turns'],
                      ['steps_per_turn', 'Steps/Turn'],
                      ['avg_latency_seconds', 'Avg Latency'],
                      ['failure_rate', 'Failure'],
                      ['cancel_rate', 'Cancel'],
                    ] as Array<[SortKey, string]>
                  ).map(([key, label]) => (
                    <th key={key} className="py-2.5 px-3 text-right font-medium">
                      <button
                        type="button"
                        onClick={() => toggleSort(key)}
                        className={`inline-flex items-center gap-1 hover:text-zinc-200 transition-colors cursor-pointer ${
                          sortKey === key ? 'text-white' : ''
                        }`}
                      >
                        <span>{label}</span>
                        <ArrowUpDown className="w-3 h-3 opacity-60" />
                      </button>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-900">
                {sortedModels.map((m) => {
                  const prov = m.provider || inferProvider(m.model);
                  const meta = PROVIDER_META[prov] ?? PROVIDER_META.other;
                  return (
                    <tr
                      key={m.model}
                      onClick={() => onSelectModel(m.model)}
                      className="hover:bg-zinc-900/70 transition-colors cursor-pointer"
                    >
                      <td className="py-2.5 px-5 font-mono text-zinc-200">
                        <span className="inline-flex items-center gap-2">
                          <span
                            className="w-2 h-2 rounded-full shrink-0"
                            style={{ backgroundColor: meta.color }}
                          />
                          <span>{m.model}</span>
                        </span>
                      </td>
                      <td className="py-2.5 px-3 text-right font-mono text-zinc-200">
                        {formatNumber(m.requests)}
                      </td>
                      <td className="py-2.5 px-3 text-right font-mono text-zinc-300">
                        {formatNumber(m.sessions)}
                      </td>
                      <td className="py-2.5 px-3 text-right font-mono text-zinc-300">
                        {formatNumber(m.turns)}
                      </td>
                      <td className="py-2.5 px-3 text-right font-mono text-zinc-400">
                        {m.steps_per_turn > 0 ? m.steps_per_turn.toFixed(1) : '—'}
                      </td>
                      <td className="py-2.5 px-3 text-right font-mono text-zinc-400">
                        {m.avg_latency_seconds > 0
                          ? formatLatencySeconds(m.avg_latency_seconds)
                          : '—'}
                      </td>
                      <td className="py-2.5 px-3 text-right font-mono text-zinc-400">
                        {formatPercent(m.failure_rate)}
                      </td>
                      <td className="py-2.5 px-3 text-right font-mono text-zinc-400">
                        {formatPercent(m.cancel_rate)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </main>
  );
}
