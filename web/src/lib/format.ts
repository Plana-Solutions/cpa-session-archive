export type Provider = 'anthropic' | 'openai' | 'google' | 'other';

export const PROVIDER_META: Record<
  Provider,
  { label: string; color: string; bgClass: string; textClass: string; borderClass: string }
> = {
  anthropic: {
    label: 'Anthropic',
    color: '#d97757',
    bgClass: 'bg-[#d97757]',
    textClass: 'text-[#d97757]',
    borderClass: 'border-[#d97757]/40',
  },
  openai: {
    label: 'OpenAI',
    color: '#ededed',
    bgClass: 'bg-[#ededed]',
    textClass: 'text-zinc-100',
    borderClass: 'border-zinc-500/40',
  },
  google: {
    label: 'Google',
    color: '#60a5fa',
    bgClass: 'bg-[#60a5fa]',
    textClass: 'text-[#60a5fa]',
    borderClass: 'border-[#60a5fa]/40',
  },
  other: {
    label: 'Other',
    color: '#71717a',
    bgClass: 'bg-zinc-500',
    textClass: 'text-zinc-400',
    borderClass: 'border-zinc-700',
  },
};

export function inferProvider(model: string): Provider {
  const m = (model || '').toLowerCase();
  if (m.startsWith('claude-')) return 'anthropic';
  if (
    m.startsWith('gpt-') ||
    m.startsWith('o1') ||
    m.startsWith('o3') ||
    m.startsWith('o4') ||
    m.startsWith('codex-')
  ) {
    return 'openai';
  }
  if (m.startsWith('gemini-')) return 'google';
  return 'other';
}

export function formatNumber(n: number | undefined | null, decimals = 0): string {
  if (n == null || !Number.isFinite(n)) return '0';
  return n.toLocaleString('en-US', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

export function formatCompact(n: number | undefined | null): string {
  if (n == null || !Number.isFinite(n)) return '0';
  const abs = Math.abs(n);
  if (abs >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(2)}B`;
  if (abs >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (abs >= 10_000) return `${(n / 1_000).toFixed(1)}k`;
  if (abs >= 1_000) return `${(n / 1_000).toFixed(2)}k`;
  return String(Math.round(n));
}

export function formatPercent(ratio: number | undefined | null): string {
  if (ratio == null || !Number.isFinite(ratio)) return '0.0%';
  return `${(ratio * 100).toFixed(1)}%`;
}

export function formatDurationMs(ms: number | undefined | null): string {
  if (ms == null || !Number.isFinite(ms) || ms <= 0) return '—';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const rem = Math.round(s % 60);
  return `${m}m ${rem}s`;
}

export function formatLatencySeconds(sec: number | undefined | null): string {
  if (sec == null || !Number.isFinite(sec) || sec <= 0) return '0.0s';
  return `${sec.toFixed(1)}s`;
}

export function formatRelativeTime(iso: string | undefined | null): string {
  if (!iso) return '—';
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return iso;
  const diffSec = Math.round((Date.now() - ts) / 1000);
  if (diffSec < 60) return `${Math.max(0, diffSec)}s ago`;
  const diffMin = Math.round(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.round(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  const diffDay = Math.round(diffHr / 24);
  if (diffDay < 30) return `${diffDay}d ago`;
  return new Date(ts).toISOString().slice(0, 10);
}

export function formatTimestamp(iso: string | undefined | null): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toISOString().replace('T', ' ').slice(0, 19) + ' UTC';
}

export function formatShortDay(day: string): string {
  // "2026-11-23" -> "Nov 23"
  const d = new Date(`${day}T00:00:00Z`);
  if (Number.isNaN(d.getTime())) return day;
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' });
}

export function normalizeKeyHash(value: string | undefined | null): string {
  return String(value || '')
    .trim()
    .toLowerCase()
    .replace(/^sha256:/, '');
}
