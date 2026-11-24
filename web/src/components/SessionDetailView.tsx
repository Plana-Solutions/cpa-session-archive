import { useEffect, useState } from 'react';
import {
  ArrowLeft,
  Copy,
  Check,
  Download,
  ChevronDown,
  ChevronRight,
  ChevronLeft,
  Wrench,
  Brain,
  Code2,
  Clock,
  AlertCircle,
  Loader2,
  User,
  Sparkles,
  X,
  ArrowUpDown,
} from 'lucide-react';
import type {
  ArchiveClient,
  ArchiveSession,
  RawRecord,
  RequestTimelineView,
  TurnDetailResponse,
  TurnSummary,
} from '../lib/api';
import {
  PROVIDER_META,
  formatCompact,
  formatDurationMs,
  formatNumber,
  formatTimestamp,
  inferProvider,
} from '../lib/format';

interface SessionDetailViewProps {
  client: ArchiveClient;
  sessionId: string;
  initialSession?: ArchiveSession | null;
  onBack: () => void;
}

const TURNS_PAGE_SIZE = 25;

function decodeRecordBytes(value: unknown): unknown {
  if (typeof value !== 'string') return value;
  const trimmed = value.trim();
  if (!trimmed) return value;
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try {
      return JSON.parse(trimmed);
    } catch {
      return value;
    }
  }
  try {
    const binary = atob(trimmed);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
    const decoded = new TextDecoder('utf-8').decode(bytes);
    const dTrim = decoded.trim();
    if (dTrim.startsWith('{') || dTrim.startsWith('[')) {
      try {
        return JSON.parse(dTrim);
      } catch {
        return decoded;
      }
    }
    return decoded;
  } catch {
    return value;
  }
}

function prettyJson(v: unknown): string {
  if (typeof v === 'string') {
    try {
      return JSON.stringify(JSON.parse(v), null, 2);
    } catch {
      return v;
    }
  }
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function decodeRawRecord(record: RawRecord): RawRecord {
  const out: RawRecord = { ...record };
  if (record.original_request !== undefined) {
    out.original_request = decodeRecordBytes(record.original_request);
  }
  if (record.upstream_request !== undefined) {
    out.upstream_request = decodeRecordBytes(record.upstream_request);
  }
  if (record.response !== undefined) {
    out.response = decodeRecordBytes(record.response);
  }
  return out;
}

export function SessionDetailView({
  client,
  sessionId,
  initialSession,
  onBack,
}: SessionDetailViewProps) {
  const [turns, setTurns] = useState<TurnSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [order, setOrder] = useState<'asc' | 'desc'>('asc');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [copiedId, setCopiedId] = useState(false);

  // Full prompt expansion cache by turn_id
  const [fullPrompts, setFullPrompts] = useState<Record<string, string>>({});
  const [loadingPrompt, setLoadingPrompt] = useState<Record<string, boolean>>({});

  // Expanded process / tool steps cache by turn_id
  const [expandedTurn, setExpandedTurn] = useState<Record<string, boolean>>({});
  const [turnDetails, setTurnDetails] = useState<Record<string, TurnDetailResponse>>({});
  const [loadingDetail, setLoadingDetail] = useState<Record<string, boolean>>({});
  const [loadingMoreSteps, setLoadingMoreSteps] = useState<Record<string, boolean>>({});
  const [stepsExhausted, setStepsExhausted] = useState<Record<string, boolean>>({});

  // Expanded request-view messages cache by request_id
  const [requestViews, setRequestViews] = useState<Record<string, RequestTimelineView>>({});
  const [loadingReqView, setLoadingReqView] = useState<Record<string, boolean>>({});

  // Tool context outputs by `${requestId}:${callId}`
  const [toolOutputs, setToolOutputs] = useState<Record<string, string>>({});
  const [loadingToolOut, setLoadingToolOut] = useState<Record<string, boolean>>({});

  // Raw JSON modal
  const [rawModal, setRawModal] = useState<{
    requestId: string;
    loading: boolean;
    record: RawRecord | null;
    error: string | null;
    tab: 'all' | 'request' | 'response';
  } | null>(null);

  // Export state
  const [exportOpen, setExportOpen] = useState(false);
  const [exportStatus, setExportStatus] = useState<string | null>(null);

  useEffect(() => {
    setOffset(0);
    setExpandedTurn({});
    setFullPrompts({});
    setTurnDetails({});
    setStepsExhausted({});
  }, [sessionId]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);

    client
      .getTurns(
        sessionId,
        { limit: TURNS_PAGE_SIZE, offset, order, days: 365 },
        controller.signal
      )
      .then((res) => {
        if (!controller.signal.aborted) {
          setTurns(res.turns ?? []);
          setTotal(res.total ?? 0);
          setLoading(false);
        }
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        setError(err instanceof Error ? err.message : 'Failed to load conversation');
        setLoading(false);
      });

    return () => controller.abort();
  }, [client, sessionId, offset, order]);

  const copySessionId = () => {
    navigator.clipboard?.writeText(sessionId).then(() => {
      setCopiedId(true);
      setTimeout(() => setCopiedId(false), 1500);
    });
  };

  const loadFullPrompt = async (turnId: string) => {
    if (fullPrompts[turnId] || loadingPrompt[turnId]) return;
    setLoadingPrompt((p) => ({ ...p, [turnId]: true }));
    try {
      const res = await client.getTurnText(sessionId, turnId);
      setFullPrompts((p) => ({ ...p, [turnId]: res.text }));
    } catch {
      // Ignore; keep truncated text.
    } finally {
      setLoadingPrompt((p) => ({ ...p, [turnId]: false }));
    }
  };

  const toggleProcess = async (turnId: string) => {
    const nextOpen = !expandedTurn[turnId];
    setExpandedTurn((p) => ({ ...p, [turnId]: nextOpen }));
    if (nextOpen && !turnDetails[turnId] && !loadingDetail[turnId]) {
      setLoadingDetail((p) => ({ ...p, [turnId]: true }));
      try {
        const detail = await client.getTurnDetail(sessionId, turnId, 0);
        setTurnDetails((p) => ({ ...p, [turnId]: detail }));
      } catch {
        // Ignore.
      } finally {
        setLoadingDetail((p) => ({ ...p, [turnId]: false }));
      }
    }
  };

  const loadMoreSteps = async (turnId: string) => {
    const current = turnDetails[turnId];
    if (!current || loadingMoreSteps[turnId] || stepsExhausted[turnId]) return;
    const nextOffset = current.records.length;
    setLoadingMoreSteps((p) => ({ ...p, [turnId]: true }));
    try {
      const next = await client.getTurnDetail(sessionId, turnId, nextOffset);
      const added = next.records ?? [];
      if (added.length === 0) {
        setStepsExhausted((p) => ({ ...p, [turnId]: true }));
      } else {
        setTurnDetails((p) => {
          const prev = p[turnId];
          if (!prev) return p;
          return {
            ...p,
            [turnId]: {
              ...next,
              records: [...prev.records, ...added],
            },
          };
        });
      }
    } catch {
      // Ignore.
    } finally {
      setLoadingMoreSteps((p) => ({ ...p, [turnId]: false }));
    }
  };

  const loadRequestView = async (requestId: string) => {
    if (requestViews[requestId] || loadingReqView[requestId]) return;
    setLoadingReqView((p) => ({ ...p, [requestId]: true }));
    try {
      const view = await client.getRequestView(requestId);
      setRequestViews((p) => ({ ...p, [requestId]: view }));
    } catch {
      // Ignore.
    } finally {
      setLoadingReqView((p) => ({ ...p, [requestId]: false }));
    }
  };

  const loadToolOutput = async (requestId: string, callId: string) => {
    const k = `${requestId}:${callId}`;
    if (toolOutputs[k] !== undefined || loadingToolOut[k]) return;
    setLoadingToolOut((p) => ({ ...p, [k]: true }));
    try {
      const recs = await client.getRequestContext(requestId, callId);
      let text = '(no tool output found)';
      const first = recs?.[0];
      const decodedResp = first?.response !== undefined ? decodeRecordBytes(first.response) : null;
      if (decodedResp && typeof decodedResp === 'object') {
        const resp = decodedResp as { output?: Array<{ output?: string }> };
        if (Array.isArray(resp.output) && resp.output.length > 0) {
          text = resp.output.map((o) => o.output ?? '').join('\n---\n');
        } else {
          text = prettyJson(decodedResp);
        }
      } else if (typeof decodedResp === 'string' && decodedResp.trim()) {
        text = decodedResp;
      } else if (first?.original_request !== undefined) {
        text = prettyJson(decodeRecordBytes(first.original_request));
      }
      setToolOutputs((p) => ({ ...p, [k]: text }));
    } catch (err) {
      setToolOutputs((p) => ({
        ...p,
        [k]: err instanceof Error ? `Error: ${err.message}` : 'Failed to load tool output',
      }));
    } finally {
      setLoadingToolOut((p) => ({ ...p, [k]: false }));
    }
  };

  const openRawRequest = async (requestId: string) => {
    setRawModal({ requestId, loading: true, record: null, error: null, tab: 'all' });
    try {
      const record = await client.getRawRequest(requestId);
      setRawModal({
        requestId,
        loading: false,
        record: decodeRawRecord(record),
        error: null,
        tab: 'all',
      });
    } catch (err) {
      setRawModal({
        requestId,
        loading: false,
        record: null,
        error: err instanceof Error ? err.message : 'Failed to load raw request',
        tab: 'all',
      });
    }
  };

  const handleExport = async (format: 'archive' | 'sft') => {
    setExportOpen(false);
    setExportStatus(`Exporting ${format.toUpperCase()}…`);
    try {
      await client.downloadExport(
        { scope: 'session', sessionId, format, days: 365 },
        (chunks, bytes) => {
          setExportStatus(`Exporting (${chunks} chunks · ${formatCompact(bytes)} B)…`);
        }
      );
      setExportStatus(null);
    } catch (err) {
      setExportStatus(null);
      setError(err instanceof Error ? err.message : 'Export failed');
    }
  };

  const isExternal = sessionId.startsWith('external:');
  const primaryModel = initialSession?.model || turns[0]?.model || '';
  const prov = inferProvider(primaryModel);
  const provMeta = PROVIDER_META[prov] ?? PROVIDER_META.other;
  const pageCount = Math.max(1, Math.ceil(total / TURNS_PAGE_SIZE));
  const currentPage = Math.floor(offset / TURNS_PAGE_SIZE) + 1;

  return (
    <main className="mx-auto max-w-[1100px] px-4 sm:px-6 py-6 space-y-6">
      {/* Sticky Subheader */}
      <div className="flex flex-col gap-3 pb-4 border-b border-zinc-800/80">
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <button
            type="button"
            onClick={onBack}
            className="inline-flex items-center gap-1.5 text-xs font-medium text-zinc-400 hover:text-white transition-colors cursor-pointer"
          >
            <ArrowLeft className="w-3.5 h-3.5" />
            <span>Back to Sessions</span>
          </button>

          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => {
                setOffset(0);
                setOrder((o) => (o === 'asc' ? 'desc' : 'asc'));
              }}
              className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-xs text-zinc-300 hover:text-white transition-colors cursor-pointer"
            >
              <ArrowUpDown className="w-3 h-3" />
              <span>{order === 'asc' ? 'Oldest first' : 'Newest first'}</span>
            </button>

            <div className="relative">
              <button
                type="button"
                onClick={() => setExportOpen((v) => !v)}
                disabled={Boolean(exportStatus)}
                className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-xs font-medium text-zinc-300 hover:text-white disabled:opacity-50 transition-colors cursor-pointer"
              >
                {exportStatus ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                ) : (
                  <Download className="w-3.5 h-3.5" />
                )}
                <span>{exportStatus || 'Export'}</span>
              </button>
              {exportOpen && (
                <div className="absolute right-0 mt-1.5 w-44 rounded-lg border border-zinc-800 bg-[#0d0d0f] p-1 shadow-xl z-20">
                  <button
                    type="button"
                    onClick={() => handleExport('archive')}
                    className="w-full text-left rounded-md px-3 py-2 text-xs text-zinc-200 hover:bg-zinc-900 cursor-pointer"
                  >
                    Archive JSONL
                  </button>
                  <button
                    type="button"
                    onClick={() => handleExport('sft')}
                    className="w-full text-left rounded-md px-3 py-2 text-xs text-zinc-200 hover:bg-zinc-900 cursor-pointer"
                  >
                    SFT Training JSONL
                  </button>
                </div>
              )}
            </div>
          </div>
        </div>

        <div>
          <h1 className="text-lg sm:text-xl font-semibold text-white leading-snug">
            {initialSession?.summary?.trim() || turns[0]?.user_text?.slice(0, 140) || sessionId}
          </h1>
          <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-zinc-400">
            <button
              type="button"
              onClick={copySessionId}
              className="inline-flex items-center gap-1.5 font-mono text-zinc-400 hover:text-zinc-200 cursor-pointer"
              title="Copy session ID"
            >
              <span>{sessionId}</span>
              {copiedId ? (
                <Check className="w-3 h-3 text-emerald-400" />
              ) : (
                <Copy className="w-3 h-3 text-zinc-600" />
              )}
            </button>
            {primaryModel && (
              <span className="inline-flex items-center gap-1.5 font-mono text-zinc-200">
                <span
                  className="w-2 h-2 rounded-full"
                  style={{ backgroundColor: provMeta.color }}
                />
                {primaryModel}
              </span>
            )}
            {initialSession?.client && <span>{initialSession.client}</span>}
            {isExternal && (
              <span className="rounded-full border border-purple-500/30 bg-purple-950/40 px-2 py-0.5 text-[10px] text-purple-300">
                Native Import
              </span>
            )}
            <span>
              {formatNumber(total)} {total === 1 ? 'turn' : 'turns'}
            </span>
          </div>
        </div>
      </div>

      {error && (
        <div
          role="alert"
          className="flex items-center gap-2.5 rounded-xl border border-red-500/30 bg-red-950/30 px-4 py-3 text-sm text-red-300"
        >
          <AlertCircle className="w-4 h-4 shrink-0 text-red-400" />
          <span>{error}</span>
        </div>
      )}

      {/* Turns Timeline */}
      {loading ? (
        <div className="space-y-4">
          {[1, 2, 3].map((n) => (
            <div
              key={n}
              className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] p-5 space-y-3"
            >
              <div className="h-4 w-1/3 rounded bg-zinc-900 animate-pulse" />
              <div className="h-16 w-full rounded bg-zinc-900/70 animate-pulse" />
            </div>
          ))}
        </div>
      ) : turns.length === 0 ? (
        <div className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] p-12 text-center text-sm text-zinc-500">
          No turns recorded for this session.
        </div>
      ) : (
        <div className="space-y-5">
          {turns.map((turn, idx) => {
            const turnNum = order === 'asc' ? offset + idx + 1 : total - (offset + idx);
            const promptText = fullPrompts[turn.turn_id] ?? turn.user_text;
            const promptBytes = turn.user_text
              ? new TextEncoder().encode(turn.user_text).length
              : 0;
            const looksTruncated =
              !fullPrompts[turn.turn_id] &&
              ((turn.user_text?.length ?? 0) >= 1590 || promptBytes >= 1590);
            const isExpanded = Boolean(expandedTurn[turn.turn_id]);
            const detail = turnDetails[turn.turn_id];
            const outcomeBad = turn.outcome === 'failed' || turn.outcome === 'rejected';
            const outcomeCancel = turn.outcome === 'canceled';

            return (
              <article
                key={turn.turn_id}
                className="rounded-xl border border-zinc-800/80 bg-[#0a0a0a] overflow-hidden"
              >
                {/* Turn Metadata Bar */}
                <div className="px-4 sm:px-5 py-2.5 border-b border-zinc-900 bg-zinc-950/70 flex flex-wrap items-center justify-between gap-2 text-xs text-zinc-500">
                  <div className="flex flex-wrap items-center gap-2.5">
                    <span className="font-mono text-zinc-400">#{turnNum}</span>
                    {turn.model && (
                      <span className="font-mono text-zinc-300">{turn.model}</span>
                    )}
                    {turn.outcome && (
                      <span
                        className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${
                          outcomeBad
                            ? 'bg-red-950/60 text-red-300 border border-red-500/30'
                            : outcomeCancel
                              ? 'bg-amber-950/50 text-amber-300 border border-amber-500/30'
                              : 'bg-zinc-900 text-zinc-400 border border-zinc-800'
                        }`}
                      >
                        {turn.outcome}
                      </span>
                    )}
                    {turn.duration_ms > 0 && (
                      <span className="inline-flex items-center gap-1">
                        <Clock className="w-3 h-3 text-zinc-600" />
                        {formatDurationMs(turn.duration_ms)}
                      </span>
                    )}
                    {turn.total_tokens > 0 && (
                      <span className="font-mono">{formatCompact(turn.total_tokens)} tok</span>
                    )}
                    {turn.requests > 1 && <span>{turn.requests} steps</span>}
                  </div>
                  <span className="font-mono text-[11px] text-zinc-600">
                    {formatTimestamp(turn.first_at)}
                  </span>
                </div>

                {/* User Prompt */}
                <div className="px-4 sm:px-5 py-4 border-b border-zinc-900/90">
                  <div className="flex items-center gap-2 text-xs font-medium text-zinc-400 mb-2">
                    <User className="w-3.5 h-3.5 text-zinc-500" />
                    <span>{turn.automated ? 'Automated / Tool Continuation' : 'User'}</span>
                  </div>
                  {promptText ? (
                    <div className="whitespace-pre-wrap break-words text-sm text-zinc-100 leading-relaxed font-sans">
                      {promptText}
                    </div>
                  ) : (
                    <p className="text-xs italic text-zinc-500">
                      No user message text in this turn.
                    </p>
                  )}
                  {looksTruncated && (
                    <button
                      type="button"
                      onClick={() => loadFullPrompt(turn.turn_id)}
                      disabled={loadingPrompt[turn.turn_id]}
                      className="mt-2.5 text-xs text-[#d97757] hover:underline cursor-pointer"
                    >
                      {loadingPrompt[turn.turn_id] ? 'Loading full prompt…' : 'Show full prompt'}
                    </button>
                  )}
                </div>

                {/* Assistant Final Reply */}
                {turn.final_text && (
                  <div className="px-4 sm:px-5 py-4 bg-black/40">
                    <div className="flex items-center gap-2 text-xs font-medium text-zinc-400 mb-2">
                      <Sparkles className="w-3.5 h-3.5 text-[#d97757]" />
                      <span>Assistant</span>
                    </div>
                    <div className="whitespace-pre-wrap break-words text-sm text-zinc-200 leading-relaxed">
                      {turn.final_text}
                    </div>
                    {turn.final_truncated && (
                      <p className="mt-2 text-xs text-zinc-500">
                        Reply preview truncated at 12,000 chars — inspect raw request below for full
                        payload.
                      </p>
                    )}
                  </div>
                )}

                {/* Tools & Process Accordion Bar */}
                <div className="px-4 sm:px-5 py-2.5 border-t border-zinc-900 bg-zinc-950/50 flex flex-wrap items-center justify-between gap-2">
                  <button
                    type="button"
                    onClick={() => toggleProcess(turn.turn_id)}
                    className="inline-flex items-center gap-1.5 text-xs font-medium text-zinc-400 hover:text-white transition-colors cursor-pointer"
                  >
                    {isExpanded ? (
                      <ChevronDown className="w-3.5 h-3.5" />
                    ) : (
                      <ChevronRight className="w-3.5 h-3.5" />
                    )}
                    <span>
                      Process &amp; tools ({turn.requests} {turn.requests === 1 ? 'step' : 'steps'})
                    </span>
                    {turn.tool_names && turn.tool_names.length > 0 && (
                      <span className="ml-1 inline-flex flex-wrap items-center gap-1">
                        {turn.tool_names.slice(0, 5).map((tool) => (
                          <span
                            key={tool}
                            className="rounded bg-zinc-900 border border-zinc-800 px-1.5 py-0.5 font-mono text-[10px] text-zinc-400"
                          >
                            {tool}
                          </span>
                        ))}
                        {turn.tool_names.length > 5 && (
                          <span className="text-[10px] text-zinc-500">
                            +{turn.tool_names.length - 5}
                          </span>
                        )}
                      </span>
                    )}
                  </button>

                  {turn.final_request && (
                    <button
                      type="button"
                      onClick={() => openRawRequest(turn.final_request!)}
                      className="inline-flex items-center gap-1 text-xs text-zinc-500 hover:text-zinc-200 cursor-pointer"
                    >
                      <Code2 className="w-3.5 h-3.5" />
                      <span>Raw JSON</span>
                    </button>
                  )}
                </div>

                {/* Expanded Turn Process Steps */}
                {isExpanded && (
                  <div className="border-t border-zinc-900 bg-black px-4 sm:px-5 py-4 space-y-3">
                    {loadingDetail[turn.turn_id] ? (
                      <div className="flex items-center gap-2 text-xs text-zinc-500">
                        <Loader2 className="w-3.5 h-3.5 animate-spin" />
                        <span>Loading steps…</span>
                      </div>
                    ) : !detail || detail.records.length === 0 ? (
                      <p className="text-xs text-zinc-500">No step records available.</p>
                    ) : (
                      detail.records.map((rec, stepIdx) => {
                        const fullView = requestViews[rec.request_id];
                        const entries = fullView?.entries ?? rec.entries ?? [];
                        return (
                          <div
                            key={rec.request_id}
                            className="rounded-lg border border-zinc-800/80 bg-[#0b0b0d] p-3.5 space-y-2.5"
                          >
                            <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
                              <div className="flex items-center gap-2 text-zinc-400">
                                <span className="font-mono text-zinc-500">
                                  Step {stepIdx + 1}
                                </span>
                                {rec.model && (
                                  <span className="font-mono text-zinc-300">{rec.model}</span>
                                )}
                                {rec.outcome && (
                                  <span className="text-zinc-500">{rec.outcome}</span>
                                )}
                              </div>
                              <div className="flex items-center gap-2">
                                {!fullView && (
                                  <button
                                    type="button"
                                    onClick={() => loadRequestView(rec.request_id)}
                                    disabled={loadingReqView[rec.request_id]}
                                    className="text-xs text-[#d97757] hover:underline cursor-pointer"
                                  >
                                    {loadingReqView[rec.request_id]
                                      ? 'Loading…'
                                      : 'Load full step messages'}
                                  </button>
                                )}
                                <button
                                  type="button"
                                  onClick={() => openRawRequest(rec.request_id)}
                                  className="inline-flex items-center gap-1 text-xs text-zinc-400 hover:text-white cursor-pointer"
                                >
                                  <Code2 className="w-3 h-3" />
                                  <span>Raw</span>
                                </button>
                              </div>
                            </div>

                            {entries.length > 0 && (
                              <div className="space-y-2">
                                {entries.map((entry, eIdx) => {
                                  const isReasoning = entry.label === 'reasoning';
                                  const isToolCall = entry.role === 'tool_call';
                                  const isToolRes = entry.role === 'tool_result';
                                  const outKey = entry.call_id
                                    ? `${rec.request_id}:${entry.call_id}`
                                    : '';

                                  return (
                                    <div
                                      key={`${rec.request_id}-${eIdx}`}
                                      className="rounded-md border border-zinc-800/70 bg-black/70 p-2.5 text-xs"
                                    >
                                      <div className="flex items-center justify-between gap-2 mb-1">
                                        <span className="inline-flex items-center gap-1.5 font-mono text-zinc-300">
                                          {isReasoning ? (
                                            <Brain className="w-3 h-3 text-purple-400" />
                                          ) : isToolCall || isToolRes ? (
                                            <Wrench className="w-3 h-3 text-[#d97757]" />
                                          ) : null}
                                          <span>
                                            {entry.label || entry.role}
                                            {entry.direction ? ` (${entry.direction})` : ''}
                                          </span>
                                        </span>
                                        {isToolCall && entry.call_id && (
                                          <button
                                            type="button"
                                            onClick={() =>
                                              loadToolOutput(rec.request_id, entry.call_id!)
                                            }
                                            disabled={loadingToolOut[outKey]}
                                            className="text-[11px] text-zinc-400 hover:text-white cursor-pointer"
                                          >
                                            {loadingToolOut[outKey]
                                              ? 'Loading result…'
                                              : toolOutputs[outKey] !== undefined
                                                ? 'Result loaded'
                                                : 'Load tool output'}
                                          </button>
                                        )}
                                      </div>

                                      {entry.text && (
                                        <pre className="whitespace-pre-wrap break-words font-mono text-[11px] text-zinc-300 max-h-72 overflow-y-auto">
                                          {entry.text}
                                        </pre>
                                      )}

                                      {isToolCall && outKey && toolOutputs[outKey] !== undefined && (
                                        <div className="mt-2 pt-2 border-t border-zinc-800">
                                          <div className="text-[10px] uppercase tracking-wider text-zinc-500 mb-1">
                                            Tool Output
                                          </div>
                                          <pre className="whitespace-pre-wrap break-words font-mono text-[11px] text-emerald-300/90 max-h-72 overflow-y-auto">
                                            {toolOutputs[outKey]}
                                          </pre>
                                        </div>
                                      )}
                                    </div>
                                  );
                                })}
                              </div>
                            )}
                          </div>
                        );
                      })
                    )}
                    {detail &&
                      detail.records.length > 0 &&
                      detail.records.length < detail.total &&
                      !stepsExhausted[turn.turn_id] && (
                        <div className="flex items-center justify-between pt-1">
                          <span className="text-[11px] text-zinc-500">
                            Showing {formatNumber(detail.records.length)} of{' '}
                            {formatNumber(detail.total)} steps
                          </span>
                          <button
                            type="button"
                            onClick={() => loadMoreSteps(turn.turn_id)}
                            disabled={loadingMoreSteps[turn.turn_id]}
                            className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-1.5 text-xs text-zinc-300 hover:border-zinc-700 hover:text-white disabled:opacity-50 cursor-pointer"
                          >
                            {loadingMoreSteps[turn.turn_id] && (
                              <Loader2 className="w-3 h-3 animate-spin" />
                            )}
                            <span>
                              {loadingMoreSteps[turn.turn_id]
                                ? 'Loading more steps…'
                                : 'Load more steps'}
                            </span>
                          </button>
                        </div>
                      )}
                  </div>
                )}
              </article>
            );
          })}
        </div>
      )}

      {/* Pagination Footer */}
      {pageCount > 1 && (
        <div className="flex items-center justify-between pt-2 text-xs text-zinc-400">
          <span>
            Page {currentPage} of {pageCount} ({formatNumber(total)} turns)
          </span>
          <div className="flex items-center gap-1.5">
            <button
              type="button"
              disabled={offset === 0}
              onClick={() => {
                setOffset((o) => Math.max(0, o - TURNS_PAGE_SIZE));
                window.scrollTo(0, 0);
              }}
              className="inline-flex items-center gap-1 rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-1.5 hover:text-white disabled:opacity-40 cursor-pointer"
            >
              <ChevronLeft className="w-3.5 h-3.5" />
              <span>Prev</span>
            </button>
            <button
              type="button"
              disabled={offset + TURNS_PAGE_SIZE >= total}
              onClick={() => {
                setOffset((o) => o + TURNS_PAGE_SIZE);
                window.scrollTo(0, 0);
              }}
              className="inline-flex items-center gap-1 rounded-lg border border-zinc-800 bg-zinc-950 px-3 py-1.5 hover:text-white disabled:opacity-40 cursor-pointer"
            >
              <span>Next</span>
              <ChevronRight className="w-3.5 h-3.5" />
            </button>
          </div>
        </div>
      )}

      {/* Raw Request JSON Modal */}
      {rawModal && (
        <div
          className="fixed inset-0 z-50 bg-black/80 backdrop-blur-xs flex items-center justify-center p-4"
          onClick={() => setRawModal(null)}
        >
          <div
            className="w-full max-w-4xl max-h-[85vh] rounded-xl border border-zinc-800 bg-[#0a0a0a] flex flex-col overflow-hidden shadow-2xl"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="px-5 py-3.5 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2 min-w-0">
                <Code2 className="w-4 h-4 text-[#d97757] shrink-0" />
                <span className="font-mono text-xs text-zinc-200 truncate">
                  {rawModal.requestId}
                </span>
              </div>
              <div className="flex items-center gap-2">
                {rawModal.record && (
                  <div className="inline-flex rounded-md border border-zinc-800 bg-black p-0.5 text-[11px]">
                    {(
                      [
                        { id: 'all', label: 'All' },
                        { id: 'request', label: 'Request' },
                        { id: 'response', label: 'Response' },
                      ] as const
                    ).map((t) => (
                      <button
                        key={t.id}
                        type="button"
                        onClick={() => setRawModal((m) => (m ? { ...m, tab: t.id } : m))}
                        className={`rounded px-2 py-0.5 cursor-pointer transition-colors ${
                          rawModal.tab === t.id
                            ? 'bg-zinc-800 text-white'
                            : 'text-zinc-400 hover:text-zinc-200'
                        }`}
                      >
                        {t.label}
                      </button>
                    ))}
                  </div>
                )}
                <button
                  type="button"
                  onClick={() => setRawModal(null)}
                  className="text-zinc-400 hover:text-white p-1 cursor-pointer"
                  aria-label="Close"
                >
                  <X className="w-4 h-4" />
                </button>
              </div>
            </div>
            <div className="p-5 overflow-auto flex-1">
              {rawModal.loading ? (
                <div className="flex items-center gap-2 text-xs text-zinc-400">
                  <Loader2 className="w-4 h-4 animate-spin" />
                  <span>Reconstructing body from ClickHouse…</span>
                </div>
              ) : rawModal.error ? (
                <div className="text-xs text-red-400">{rawModal.error}</div>
              ) : (
                <pre className="whitespace-pre-wrap break-words font-mono text-xs text-zinc-200">
                  {prettyJson(
                    rawModal.tab === 'request'
                      ? rawModal.record?.original_request ?? null
                      : rawModal.tab === 'response'
                        ? rawModal.record?.response ?? null
                        : rawModal.record
                  )}
                </pre>
              )}
            </div>
          </div>
        </div>
      )}
    </main>
  );
}
