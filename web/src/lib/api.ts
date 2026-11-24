import type { AuthSession } from './auth';
import { normalizeKeyHash, type Provider } from './format';

export interface ArchiveStats {
  records: number;
  sessions: number;
}

export interface FacetCount {
  name: string;
  value: string;
  sessions: number;
  records: number;
}

export interface InsightTotals {
  requests: number;
  sessions: number;
  turns: number;
  models: number;
}

export interface ModelInsight {
  model: string;
  provider: Provider;
  requests: number;
  sessions: number;
  turns: number;
  steps_per_turn: number;
  avg_latency_seconds: number;
  failure_rate: number;
  cancel_rate: number;
}

export interface DailyInsight {
  day: string;
  requests: Partial<Record<Provider, number>>;
}

export interface InsightsResponse {
  window_days: number;
  from: string;
  to: string;
  generated_at: string;
  totals: InsightTotals;
  models: ModelInsight[];
  daily: DailyInsight[];
}

export interface ArchiveSession {
  system?: boolean;
  session_id: string;
  continued?: boolean;
  summary: string;
  project: string;
  key_id: string;
  model: string;
  client: string;
  requests: number;
  first_at: string;
  last_at: string;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  source_kind?: string;
  source_tool?: string;
}

export interface TurnSummary {
  turn_id: string;
  session_id: string;
  user_text: string;
  final_text: string;
  final_request?: string;
  final_truncated?: boolean;
  continued?: boolean;
  automated?: boolean;
  reasoning?: string;
  requests: number;
  first_at: string;
  last_at: string;
  key_id: string;
  model: string;
  outcome: string;
  status_code: number;
  tool_names?: string[];
  duration_ms: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  client?: string;
  kind?: string;
}

export interface TimelineEntry {
  role: string;
  label?: string;
  call_id?: string;
  text?: string;
  direction?: string;
  timestamp?: string;
  truncated?: boolean;
}

export interface RequestTimelineView {
  request_id: string;
  session_id: string;
  key_id?: string;
  summary?: string;
  model?: string;
  outcome?: string;
  status_code?: number;
  started_at?: string;
  completed_at?: string;
  kind?: string;
  input_items?: number;
  tool_definitions?: number;
  system_chars?: number;
  entries?: TimelineEntry[];
  facets?: Record<string, string[]>;
  metadata?: Record<string, unknown>;
}

export interface TurnsListResponse {
  turns: TurnSummary[];
  total: number;
  limit: number;
  offset: number;
}

export interface TurnDetailResponse {
  turn: TurnSummary;
  records: RequestTimelineView[];
  total: number;
  limit: number;
  offset: number;
}

export interface RawRecord {
  request_id?: string;
  session_id?: string;
  source_format?: string;
  model?: string;
  outcome?: string;
  status_code?: number;
  started_at?: string;
  completed_at?: string;
  original_request?: unknown;
  response?: unknown;
  headers?: Record<string, string>;
  facets?: Record<string, string[]>;
  metadata?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface ExportChunkResponse {
  content: string;
  done: boolean;
  next_offset?: number;
  next_session_offset?: number;
  next_byte_offset?: number;
  filename?: string;
  from?: string;
  to?: string;
  skipped_incomplete?: number;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

const PLUGIN_BASE = '/v0/management/plugins/cpa-session-archive';

async function fetchJson<T>(
  session: AuthSession,
  path: string,
  signal?: AbortSignal
): Promise<T> {
  const res = await fetch(`${session.base}${path}`, {
    headers: { Authorization: `Bearer ${session.key}` },
    signal,
  });
  if (!res.ok) {
    let detail = `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) detail = body.error;
    } catch {
      // Non-JSON error body.
    }
    throw new ApiError(res.status, detail);
  }
  return (await res.json()) as T;
}

export function createArchiveClient(session: AuthSession) {
  const searchClientId =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `ui-${Math.random().toString(36).slice(2, 10)}`;

  return {
    searchClientId,

    getStats(days = 365, signal?: AbortSignal) {
      return fetchJson<ArchiveStats>(session, `${PLUGIN_BASE}/stats?days=${days}`, signal);
    },

    getFacets(days = 365, signal?: AbortSignal) {
      return fetchJson<FacetCount[]>(session, `${PLUGIN_BASE}/facets?days=${days}`, signal);
    },

    getInsights(
      params: { days?: number; from?: string; to?: string; minRequests?: number },
      signal?: AbortSignal
    ) {
      const q = new URLSearchParams();
      if (params.from && params.to) {
        q.set('from', params.from);
        q.set('to', params.to);
      } else {
        q.set('days', String(params.days ?? 30));
      }
      if (params.minRequests && params.minRequests > 1) {
        q.set('min_requests', String(params.minRequests));
      }
      return fetchJson<InsightsResponse>(session, `${PLUGIN_BASE}/insights?${q.toString()}`, signal);
    },

    getSessions(
      params: {
        q?: string;
        days?: number;
        showSystem?: boolean;
        limit?: number;
        offset?: number;
        facets?: Record<string, string>;
      },
      signal?: AbortSignal
    ) {
      const q = new URLSearchParams();
      q.set('limit', String(params.limit ?? 50));
      q.set('offset', String(params.offset ?? 0));
      if (params.days) q.set('days', String(params.days));
      if (params.showSystem) q.set('show_system', '1');
      if (params.q && params.q.trim()) {
        q.set('q', params.q.trim());
        q.set('search_client', searchClientId);
      }
      if (params.facets) {
        for (const [k, v] of Object.entries(params.facets)) {
          if (v) q.set(k, v);
        }
      }
      return fetchJson<ArchiveSession[]>(session, `${PLUGIN_BASE}/sessions?${q.toString()}`, signal);
    },

    cancelSearch() {
      fetch(
        `${session.base}${PLUGIN_BASE}/cancel-search?search_client=${encodeURIComponent(searchClientId)}`,
        {
          headers: { Authorization: `Bearer ${session.key}` },
          keepalive: true,
        }
      ).catch(() => {});
    },

    getTurns(
      sessionId: string,
      opts: { limit?: number; offset?: number; order?: 'asc' | 'desc'; days?: number } = {},
      signal?: AbortSignal
    ) {
      const q = new URLSearchParams({
        session_id: sessionId,
        limit: String(opts.limit ?? 30),
        offset: String(opts.offset ?? 0),
        order: opts.order ?? 'asc',
        days: String(opts.days ?? 365),
      });
      return fetchJson<TurnsListResponse>(session, `${PLUGIN_BASE}/turns?${q.toString()}`, signal);
    },

    getTurnDetail(sessionId: string, turnId: string, signal?: AbortSignal) {
      const q = new URLSearchParams({
        session_id: sessionId,
        turn_id: turnId,
        limit: '100',
        days: '365',
      });
      return fetchJson<TurnDetailResponse>(session, `${PLUGIN_BASE}/turns?${q.toString()}`, signal);
    },

    getTurnText(sessionId: string, turnId: string, signal?: AbortSignal) {
      const q = new URLSearchParams({
        session_id: sessionId,
        turn_id: turnId,
        days: '365',
      });
      return fetchJson<{ status: string; text: string }>(
        session,
        `${PLUGIN_BASE}/turn-text?${q.toString()}`,
        signal
      );
    },

    getRequestView(requestId: string, signal?: AbortSignal) {
      const q = new URLSearchParams({ id: requestId, days: '365' });
      return fetchJson<RequestTimelineView>(
        session,
        `${PLUGIN_BASE}/request-view?${q.toString()}`,
        signal
      );
    },

    getRequestContext(requestId: string, callId: string, signal?: AbortSignal) {
      const q = new URLSearchParams({ id: requestId, call_id: callId, days: '365' });
      return fetchJson<RawRecord[]>(
        session,
        `${PLUGIN_BASE}/request-context?${q.toString()}`,
        signal
      );
    },

    getRawRequest(requestId: string, signal?: AbortSignal) {
      const q = new URLSearchParams({ id: requestId, days: '365' });
      return fetchJson<RawRecord>(session, `${PLUGIN_BASE}/requests?${q.toString()}`, signal);
    },

    async loadKeyAliases(): Promise<Record<string, string>> {
      const map: Record<string, string> = {};
      const safe = async <T,>(path: string): Promise<T | null> => {
        try {
          return await fetchJson<T>(session, path);
        } catch {
          return null;
        }
      };

      const [aliasesRes, identitiesRes] = await Promise.all([
        safe<{ items?: Array<{ apiKeyHash?: string; api_key_hash?: string; alias?: string }> }>(
          '/v0/management/api-key-aliases'
        ),
        safe<{
          identities?: Array<{ key_hash?: string; key_preview?: string; caller_scope?: string }>;
        }>('/v0/management/plugins/cpa-key-policy/identities'),
      ]);

      for (const item of aliasesRes?.items ?? []) {
        const hash = normalizeKeyHash(item.apiKeyHash || item.api_key_hash);
        if (hash && item.alias) {
          map[hash] = item.alias;
          map[hash.slice(0, 16)] = item.alias;
        }
      }
      for (const id of identitiesRes?.identities ?? []) {
        const alias = map[normalizeKeyHash(id.key_hash)] || id.key_preview;
        if (alias && id.caller_scope) {
          map[normalizeKeyHash(id.caller_scope)] = alias;
        }
      }
      return map;
    },

    async downloadExport(
      opts: { scope: 'session' | 'all'; sessionId?: string; format: 'archive' | 'sft'; days?: number },
      onProgress?: (chunks: number, bytes: number) => void
    ): Promise<void> {
      const parts: string[] = [];
      let offset = 0;
      let sessionOffset = 0;
      let byteOffset = 0;
      let totalBytes = 0;
      let chunks = 0;
      let filename =
        opts.scope === 'all'
          ? `cpa-archive-${opts.format}.jsonl`
          : `${(opts.sessionId || 'session').replace(/[^a-zA-Z0-9._-]+/g, '_')}-${opts.format}.jsonl`;

      let pinnedFrom = '';
      let pinnedTo = '';

      while (chunks < 5000) {
        const q = new URLSearchParams({
          format: opts.format,
          offset: String(offset),
          session_offset: String(sessionOffset),
          byte_offset: String(byteOffset),
        });
        if (pinnedFrom && pinnedTo) {
          q.set('from', pinnedFrom);
          q.set('to', pinnedTo);
        } else {
          q.set('days', String(opts.days ?? 365));
        }
        if (opts.scope === 'all') {
          q.set('scope', 'all');
        } else if (opts.sessionId) {
          q.set('id', opts.sessionId);
        }

        const res = await fetchJson<ExportChunkResponse>(
          session,
          `${PLUGIN_BASE}/export?${q.toString()}`
        );
        if (res.filename) filename = res.filename;
        if (!pinnedFrom && res.from) pinnedFrom = res.from;
        if (!pinnedTo && res.to) pinnedTo = res.to;
        if (res.content) {
          parts.push(res.content);
          totalBytes += res.content.length;
        }
        chunks++;
        onProgress?.(chunks, totalBytes);

        if (res.done) break;
        const nextOff = res.next_offset ?? offset;
        const nextSess = res.next_session_offset ?? sessionOffset;
        const nextByte = res.next_byte_offset ?? 0;
        if (nextOff === offset && nextSess === sessionOffset && nextByte === byteOffset) {
          break;
        }
        offset = nextOff;
        sessionOffset = nextSess;
        byteOffset = nextByte;
      }

      const blob = new Blob(parts, { type: 'application/x-ndjson;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 5000);
    },
  };
}

export type ArchiveClient = ReturnType<typeof createArchiveClient>;
