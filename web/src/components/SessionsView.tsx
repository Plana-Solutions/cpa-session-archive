import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Search,
  SlidersHorizontal,
  X,
  Download,
  ChevronLeft,
  ChevronRight,
  Cpu,
  Clock,
  Layers,
  HardDrive,
  AlertCircle,
  Loader2,
  Bot,
} from 'lucide-react';
import type { ArchiveClient, ArchiveSession, FacetCount } from '../lib/api';
import {
  PROVIDER_META,
  formatCompact,
  formatNumber,
  formatRelativeTime,
  formatTimestamp,
  inferProvider,
  normalizeKeyHash,
} from '../lib/format';

interface SessionsViewProps {
  client: ArchiveClient;
  facets: FacetCount[];
  keyAliases: Record<string, string>;
  activeFacets: Record<string, string>;
  onChangeFacets: (next: Record<string, string>) => void;
  onOpenSession: (session: ArchiveSession) => void;
}

const FACET_LABELS: Record<string, string> = {
  'model.resolved': 'Model',
  'model.requested': 'Requested Model',
  client: 'Client',
  outcome: 'Outcome',
  'key.id': 'API Key',
  'tool.name': 'Tool',
  'project.name': 'Project',
  'request.kind': 'Request Kind',
  'thread.source': 'Thread Source',
};

const FACET_ORDER = [
  'model.resolved',
  'client',
  'outcome',
  'key.id',
  'tool.name',
  'project.name',
  'model.requested',
  'request.kind',
  'thread.source',
];

const PAGE_SIZE = 50;

export function SessionsView({
  client,
  facets,
  keyAliases,
  activeFacets,
  onChangeFacets,
  onOpenSession,
}: SessionsViewProps) {
  const [searchInput, setSearchInput] = useState('');
  const [committedSearch, setCommittedSearch] = useState('');
  const [days, setDays] = useState<number>(30);
  const [showSystem, setShowSystem] = useState(false);
  const [offset, setOffset] = useState(0);
  const [filtersOpen, setFiltersOpen] = useState(false);

  const [sessions, setSessions] = useState<ArchiveSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [exportOpen, setExportOpen] = useState(false);
  const [exportStatus, setExportStatus] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const keyLabel = (value: string) => {
    const norm = normalizeKeyHash(value);
    return keyAliases[norm] || keyAliases[norm.slice(0, 16)] || value.slice(0, 12);
  };

  // Debounce search input cleanly.
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setOffset(0);
      setCommittedSearch(searchInput.trim());
    }, 350);
    return () => window.clearTimeout(timer);
  }, [searchInput]);

  // Reset pagination whenever filters change.
  const facetKey = useMemo(() => JSON.stringify(activeFacets), [activeFacets]);
  useEffect(() => {
    setOffset(0);
  }, [facetKey, days, showSystem]);

  useEffect(() => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setLoading(true);
    setError(null);

    client
      .getSessions(
        {
          q: committedSearch,
          days,
          showSystem,
          limit: PAGE_SIZE,
          offset,
          facets: activeFacets,
        },
        controller.signal
      )
      .then((rows) => {
        if (!controller.signal.aborted) {
          setSessions(rows ?? []);
          setLoading(false);
        }
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        setError(err instanceof Error ? err.message : 'Failed to load sessions');
        setLoading(false);
      });

    return () => {
      controller.abort();
      if (committedSearch) {
        client.cancelSearch();
      }
    };
  }, [client, committedSearch, days, showSystem, offset, facetKey]);

  const groupedFacets = useMemo(() => {
    const map = new Map<string, FacetCount[]>();
    for (const f of facets) {
      if (!f.value) continue;
      const list = map.get(f.name) ?? [];
      list.push(f);
      map.set(f.name, list);
    }
    const ordered: Array<{ name: string; label: string; values: FacetCount[] }> = [];
    for (const name of FACET_ORDER) {
      const vals = map.get(name);
      if (vals && vals.length > 0) {
        ordered.push({ name, label: FACET_LABELS[name] ?? name, values: vals });
        map.delete(name);
      }
    }
    for (const [name, vals] of map.entries()) {
      if (vals.length > 0) {
        ordered.push({ name, label: FACET_LABELS[name] ?? name, values: vals });
      }
    }
    return ordered;
  }, [facets]);

  const activeFacetEntries = Object.entries(activeFacets).filter(([, v]) => Boolean(v));

  const setFacet = (name: string, value: string) => {
    const next = { ...activeFacets };
    if (!value) {
      delete next[name];
    } else {
      next[name] = value;
    }
    onChangeFacets(next);
  };

  const handleExportAll = async (format: 'archive' | 'sft') => {
    setExportOpen(false);
    setExportStatus(`Exporting ${format.toUpperCase()}…`);
    setError(null);
    try {
      const res = await client.downloadExport({ scope: 'all', format, days }, (chunks, bytes) => {
        setExportStatus(`Exporting (${chunks} chunks · ${formatCompact(bytes)} B)…`);
      });
      setExportStatus(null);
      if (res.skippedIncomplete > 0) {
        setError(
          `Export downloaded, but ${formatNumber(res.skippedIncomplete)} incomplete record${
            res.skippedIncomplete === 1 ? ' was' : 's were'
          } skipped.`
        );
      }
    } catch (err) {
      setExportStatus(null);
      setError(err instanceof Error ? err.message : 'Export failed');
    }
  };

  return (
    <main className="mx-auto max-w-[1440px] px-4 sm:px-6 py-6 sm:py-8 space-y-5">
      {/* Header */}
      <div className="flex flex-col sm:flex-row sm:items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight text-white">Sessions</h1>
          <p className="mt-1 text-sm text-zinc-400">
            Search conversations, inspect tool calls, and browse imported native transcripts.
          </p>
        </div>

        <div className="flex items-center gap-2">
          {exportStatus && (
            <span className="inline-flex items-center gap-1.5 text-xs text-zinc-400">
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
              {exportStatus}
            </span>
          )}
          <div className="relative">
            <button
              type="button"
              onClick={() => setExportOpen((v) => !v)}
              disabled={Boolean(exportStatus)}
              className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-1.5 text-xs font-medium text-zinc-300 hover:text-white hover:border-zinc-700 disabled:opacity-50 transition-colors cursor-pointer"
            >
              <Download className="w-3.5 h-3.5" />
              <span>Export</span>
            </button>
            {exportOpen && (
              <div className="absolute right-0 mt-1.5 w-48 rounded-lg border border-zinc-800 bg-[#0d0d0f] p-1 shadow-xl z-20">
                <button
                  type="button"
                  onClick={() => handleExportAll('archive')}
                  className="w-full text-left rounded-md px-3 py-2 text-xs text-zinc-200 hover:bg-zinc-900 cursor-pointer"
                >
                  Archive JSONL ({days}d)
                </button>
                <button
                  type="button"
                  onClick={() => handleExportAll('sft')}
                  className="w-full text-left rounded-md px-3 py-2 text-xs text-zinc-200 hover:bg-zinc-900 cursor-pointer"
                >
                  SFT Training JSONL ({days}d)
                </button>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Search & Filter Toolbar */}
      <div className="flex flex-col md:flex-row items-stretch md:items-center gap-2.5">
        <div className="relative flex-1">
          <Search className="w-4 h-4 text-zinc-500 absolute left-3.5 top-1/2 -translate-y-1/2 pointer-events-none" />
          <input
            type="search"
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                setOffset(0);
                setCommittedSearch(searchInput.trim());
              }
            }}
            placeholder="Search prompts, titles, projects, or session IDs…"
            className="w-full rounded-lg border border-zinc-800 bg-[#0a0a0a] pl-10 pr-9 py-2 text-sm text-zinc-100 placeholder:text-zinc-500 focus:border-zinc-600 focus:outline-none transition-colors"
          />
          {searchInput && (
            <button
              type="button"
              onClick={() => {
                setSearchInput('');
                setCommittedSearch('');
              }}
              aria-label="Clear search"
              className="absolute right-2.5 top-1/2 -translate-y-1/2 text-zinc-500 hover:text-zinc-200 p-1 cursor-pointer"
            >
              <X className="w-3.5 h-3.5" />
            </button>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <div className="inline-flex items-center rounded-lg border border-zinc-800 bg-zinc-950 p-0.5">
            {[7, 30, 90, 365].map((d) => (
              <button
                key={d}
                type="button"
                onClick={() => setDays(d)}
                className={`rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors cursor-pointer ${
                  days === d
                    ? 'bg-zinc-800 text-white'
                    : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900'
                }`}
              >
                {d === 365 ? '1y' : `${d}d`}
              </button>
            ))}
          </div>

          <button
            type="button"
            onClick={() => setShowSystem((s) => !s)}
            className={`inline-flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors cursor-pointer ${
              showSystem
                ? 'border-zinc-600 bg-zinc-800 text-white'
                : 'border-zinc-800 bg-zinc-950 text-zinc-400 hover:text-zinc-200'
            }`}
          >
            <Bot className="w-3.5 h-3.5" />
            <span>System</span>
          </button>

          <button
            type="button"
            onClick={() => setFiltersOpen((o) => !o)}
            className={`inline-flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors cursor-pointer ${
              filtersOpen || activeFacetEntries.length > 0
                ? 'border-zinc-600 bg-zinc-900 text-white'
                : 'border-zinc-800 bg-zinc-950 text-zinc-300 hover:text-white'
            }`}
          >
            <SlidersHorizontal className="w-3.5 h-3.5" />
            <span>Filters</span>
            {activeFacetEntries.length > 0 && (
              <span className="ml-0.5 rounded-full bg-[#d97757] px-1.5 py-0.2 text-[10px] font-semibold text-black">
                {activeFacetEntries.length}
              </span>
            )}
          </button>
        </div>
      </div>

      {/* Expandable Clean Filter Drawer */}
      {filtersOpen && (
        <div className="rounded-xl border border-zinc-800 bg-[#0a0a0a] p-4 space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-xs font-semibold uppercase tracking-wider text-zinc-400">
              Filter by metadata
            </span>
            {activeFacetEntries.length > 0 && (
              <button
                type="button"
                onClick={() => onChangeFacets({})}
                className="text-xs text-zinc-400 hover:text-white cursor-pointer"
              >
                Clear all
              </button>
            )}
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
            {groupedFacets.map((group) => (
              <label key={group.name} className="block">
                <span className="block text-[11px] font-medium text-zinc-500 mb-1">
                  {group.label}
                </span>
                <select
                  data-facet={group.name}
                  value={activeFacets[group.name] ?? ''}
                  onChange={(e) => setFacet(group.name, e.target.value)}
                  className="w-full rounded-lg border border-zinc-800 bg-black px-2.5 py-1.5 text-xs text-zinc-200 focus:border-zinc-600 focus:outline-none"
                >
                  <option value="">All ({group.values.length})</option>
                  {group.values.map((v) => (
                    <option key={v.value} value={v.value}>
                      {group.name === 'key.id' ? keyLabel(v.value) : v.value} (
                      {formatCompact(v.sessions)})
                    </option>
                  ))}
                </select>
              </label>
            ))}
          </div>
        </div>
      )}

      {/* Active Filter Chips */}
      {activeFacetEntries.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {activeFacetEntries.map(([name, value]) => (
            <span
              key={name}
              className="inline-flex items-center gap-1.5 rounded-full border border-zinc-700 bg-zinc-900 px-2.5 py-1 text-xs text-zinc-200"
            >
              <span className="text-zinc-500">{FACET_LABELS[name] ?? name}:</span>
              <span className="font-mono">{name === 'key.id' ? keyLabel(value) : value}</span>
              <button
                type="button"
                onClick={() => setFacet(name, '')}
                aria-label={`Remove ${name} filter`}
                className="text-zinc-400 hover:text-white cursor-pointer"
              >
                <X className="w-3 h-3" />
              </button>
            </span>
          ))}
          <button
            type="button"
            onClick={() => onChangeFacets({})}
            className="text-xs text-zinc-500 hover:text-zinc-200 px-1 cursor-pointer"
          >
            Reset
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

      {/* Sessions List */}
      <div className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] overflow-hidden">
        <div className="px-4 sm:px-5 py-3 border-b border-zinc-800/80 flex items-center justify-between text-xs text-zinc-500">
          <span>
            {loading
              ? 'Querying ClickHouse…'
              : sessions.length === 0
                ? 'No sessions match'
                : `Showing ${offset + 1}–${offset + sessions.length}${
                    sessions.length === PAGE_SIZE ? '+' : ''
                  } sessions`}
          </span>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              disabled={offset === 0 || loading}
              onClick={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
              className="inline-flex items-center gap-1 rounded-md border border-zinc-800 bg-black px-2 py-1 text-zinc-300 hover:text-white disabled:opacity-40 cursor-pointer"
            >
              <ChevronLeft className="w-3.5 h-3.5" />
              <span>Prev</span>
            </button>
            <button
              type="button"
              disabled={sessions.length < PAGE_SIZE || loading}
              onClick={() => setOffset((o) => o + PAGE_SIZE)}
              className="inline-flex items-center gap-1 rounded-md border border-zinc-800 bg-black px-2 py-1 text-zinc-300 hover:text-white disabled:opacity-40 cursor-pointer"
            >
              <span>Next</span>
              <ChevronRight className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        {loading && sessions.length === 0 ? (
          <div className="divide-y divide-zinc-900">
            {[1, 2, 3, 4, 5, 6].map((n) => (
              <div key={n} className="px-5 py-4 space-y-2">
                <div className="h-4 w-2/3 rounded bg-zinc-900 animate-pulse" />
                <div className="h-3 w-1/3 rounded bg-zinc-900/70 animate-pulse" />
              </div>
            ))}
          </div>
        ) : sessions.length === 0 ? (
          <div className="px-6 py-16 text-center">
            <p className="text-sm text-zinc-400">No sessions found for the current filters.</p>
            {(committedSearch || activeFacetEntries.length > 0 || days < 365) && (
              <button
                type="button"
                onClick={() => {
                  setSearchInput('');
                  setCommittedSearch('');
                  onChangeFacets({});
                  setDays(365);
                }}
                className="mt-3 rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-1.5 text-xs text-zinc-300 hover:text-white cursor-pointer"
              >
                Expand to 1y and clear filters
              </button>
            )}
          </div>
        ) : (
          <ul className="divide-y divide-zinc-900">
            {sessions.map((s) => {
              const isExternal = s.session_id.startsWith('external:');
              const prov = inferProvider(s.model);
              const meta = PROVIDER_META[prov] ?? PROVIDER_META.other;
              const title = s.summary?.trim() || s.project?.trim() || s.session_id;

              return (
                <li key={s.session_id}>
                  <button
                    type="button"
                    onClick={() => onOpenSession(s)}
                    className="w-full text-left px-4 sm:px-5 py-3.5 hover:bg-zinc-900/70 transition-colors cursor-pointer group"
                  >
                    <div className="flex items-start justify-between gap-4">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2 flex-wrap">
                          <span className="text-sm font-medium text-zinc-100 group-hover:text-white line-clamp-1">
                            {title}
                          </span>
                          {isExternal && (
                            <span className="inline-flex items-center gap-1 rounded-full border border-purple-500/30 bg-purple-950/40 px-2 py-0.5 text-[10px] font-medium text-purple-300 shrink-0">
                              <HardDrive className="w-2.5 h-2.5" />
                              Native
                            </span>
                          )}
                          {s.continued && (
                            <span className="rounded-full border border-zinc-700 bg-zinc-900 px-2 py-0.5 text-[10px] text-zinc-400 shrink-0">
                              Continued
                            </span>
                          )}
                          {s.system && (
                            <span className="rounded-full border border-zinc-800 bg-zinc-950 px-2 py-0.5 text-[10px] text-zinc-500 shrink-0">
                              System
                            </span>
                          )}
                        </div>

                        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-zinc-500">
                          {s.model && (
                            <span className="inline-flex items-center gap-1.5 font-mono text-zinc-300">
                              <span
                                className="w-1.5 h-1.5 rounded-full"
                                style={{ backgroundColor: meta.color }}
                              />
                              {s.model}
                            </span>
                          )}
                          {s.client && (
                            <span className="inline-flex items-center gap-1 text-zinc-400">
                              <Cpu className="w-3 h-3 text-zinc-600" />
                              {s.client}
                            </span>
                          )}
                          {s.project && (
                            <span className="font-mono text-zinc-400 truncate max-w-[220px]">
                              {s.project}
                            </span>
                          )}
                          {s.key_id && (
                            <span className="rounded bg-zinc-900 border border-zinc-800 px-1.5 py-0.2 font-mono text-[11px] text-zinc-400">
                              {keyLabel(s.key_id)}
                            </span>
                          )}
                          <span className="inline-flex items-center gap-1">
                            <Layers className="w-3 h-3 text-zinc-600" />
                            {formatNumber(s.requests)} {isExternal ? 'events' : 'reqs'}
                          </span>
                          {s.total_tokens > 0 && (
                            <span className="font-mono text-zinc-400">
                              {formatCompact(s.total_tokens)} tok
                            </span>
                          )}
                        </div>
                      </div>

                      <div
                        className="text-right shrink-0 text-xs text-zinc-500"
                        title={formatTimestamp(s.last_at)}
                      >
                        <span className="inline-flex items-center gap-1">
                          <Clock className="w-3 h-3 text-zinc-600" />
                          {formatRelativeTime(s.last_at)}
                        </span>
                      </div>
                    </div>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </main>
  );
}
