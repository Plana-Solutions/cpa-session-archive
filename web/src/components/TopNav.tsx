import { BarChart3, MessageSquareText, ExternalLink, LogOut, Database } from 'lucide-react';
import { isEmbeddedInIframe, standaloneUrl, type AuthSession } from '../lib/auth';
import { formatCompact } from '../lib/format';
import type { ArchiveStats } from '../lib/api';

interface TopNavProps {
  activeTab: 'insights' | 'sessions';
  stats: ArchiveStats | null;
  session: AuthSession;
  onNavigate: (hash: string) => void;
  onSignOut: () => void;
}

export function TopNav({ activeTab, stats, session, onNavigate, onSignOut }: TopNavProps) {
  const embedded = isEmbeddedInIframe();

  return (
    <header className="sticky top-0 z-30 border-b border-zinc-800/80 bg-black/90 backdrop-blur-md">
      <div className="mx-auto max-w-[1440px] px-4 sm:px-6 h-14 flex items-center justify-between gap-4">
        <div className="flex items-center gap-4 sm:gap-6 min-w-0">
          <button
            type="button"
            onClick={() => onNavigate('#/insights')}
            className="flex items-center gap-2.5 text-left group cursor-pointer shrink-0"
          >
            <span className="h-2.5 w-2.5 rounded-full bg-[#d97757] ring-4 ring-[#d97757]/15" />
            <span className="text-sm font-semibold tracking-tight text-white">Fleethouse</span>
          </button>

          <nav className="flex items-center gap-1 rounded-lg bg-zinc-950 border border-zinc-800/90 p-1">
            <button
              type="button"
              onClick={() => onNavigate('#/insights')}
              aria-current={activeTab === 'insights' ? 'page' : undefined}
              className={`inline-flex items-center gap-1.5 rounded-md px-3 py-1 text-xs font-medium transition-colors cursor-pointer ${
                activeTab === 'insights'
                  ? 'bg-zinc-800 text-white'
                  : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900'
              }`}
            >
              <BarChart3 className="w-3.5 h-3.5" />
              <span>Insights</span>
            </button>
            <button
              type="button"
              onClick={() => onNavigate('#/sessions')}
              aria-current={activeTab === 'sessions' ? 'page' : undefined}
              className={`inline-flex items-center gap-1.5 rounded-md px-3 py-1 text-xs font-medium transition-colors cursor-pointer ${
                activeTab === 'sessions'
                  ? 'bg-zinc-800 text-white'
                  : 'text-zinc-400 hover:text-zinc-200 hover:bg-zinc-900'
              }`}
            >
              <MessageSquareText className="w-3.5 h-3.5" />
              <span>Sessions</span>
            </button>
          </nav>
        </div>

        <div className="flex items-center gap-2.5 shrink-0">
          <div className="hidden md:flex items-center gap-2 rounded-full border border-zinc-800/90 bg-zinc-950 px-3 py-1 text-xs text-zinc-400">
            <Database className="w-3 h-3 text-emerald-400" />
            <span className="text-zinc-300 font-medium">ClickHouse</span>
            {stats && (
              <>
                <span className="text-zinc-700">·</span>
                <span>{formatCompact(stats.sessions)} sessions</span>
                <span className="text-zinc-700">·</span>
                <span>{formatCompact(stats.records)} rows</span>
              </>
            )}
          </div>

          {embedded && (
            <a
              href={standaloneUrl()}
              target="_blank"
              rel="noopener noreferrer"
              title="Open full-screen standalone"
              className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-xs font-medium text-zinc-300 hover:text-white hover:border-zinc-700 hover:bg-zinc-900 transition-colors"
            >
              <span className="hidden sm:inline">Standalone</span>
              <ExternalLink className="w-3.5 h-3.5" />
            </a>
          )}

          {session.source === 'manual' && (
            <button
              type="button"
              onClick={onSignOut}
              title="Sign out"
              className="inline-flex items-center gap-1.5 rounded-lg border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-xs text-zinc-400 hover:text-white hover:border-zinc-700 transition-colors cursor-pointer"
            >
              <LogOut className="w-3.5 h-3.5" />
            </button>
          )}
        </div>
      </div>
    </header>
  );
}
