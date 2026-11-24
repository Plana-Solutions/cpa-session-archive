import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  clearManualSession,
  readAutoAuth,
  saveManualSession,
  type AuthSession,
} from './lib/auth';
import {
  ApiError,
  createArchiveClient,
  type ArchiveSession,
  type ArchiveStats,
  type FacetCount,
} from './lib/api';
import { LoginView } from './components/LoginView';
import { TopNav } from './components/TopNav';
import { InsightsView } from './components/InsightsView';
import { SessionsView } from './components/SessionsView';
import { SessionDetailView } from './components/SessionDetailView';

type Route =
  | { view: 'insights' }
  | { view: 'sessions' }
  | { view: 'session'; sessionId: string };

function parseHash(hash: string): Route {
  const clean = (hash || '').replace(/^#/, '');
  if (clean.startsWith('/sessions/')) {
    const rest = clean.slice('/sessions/'.length).split('/')[0];
    if (rest) {
      try {
        return { view: 'session', sessionId: decodeURIComponent(rest) };
      } catch {
        return { view: 'session', sessionId: rest };
      }
    }
  }
  if (clean === '/sessions' || clean.startsWith('/sessions?')) {
    return { view: 'sessions' };
  }
  return { view: 'insights' };
}

export function App() {
  const [session, setSession] = useState<AuthSession | null>(() => readAutoAuth());
  const [verified, setVerified] = useState(false);
  const [connecting, setConnecting] = useState(false);
  const [authError, setAuthError] = useState<string | null>(null);

  const [stats, setStats] = useState<ArchiveStats | null>(null);
  const [facets, setFacets] = useState<FacetCount[]>([]);
  const [keyAliases, setKeyAliases] = useState<Record<string, string>>({});

  const [route, setRoute] = useState<Route>(() => parseHash(window.location.hash));
  const [activeFacets, setActiveFacets] = useState<Record<string, string>>({});
  const [selectedSession, setSelectedSession] = useState<ArchiveSession | null>(null);

  const client = useMemo(() => (session ? createArchiveClient(session) : null), [session]);

  useEffect(() => {
    const onHashChange = () => {
      setRoute(parseHash(window.location.hash));
      window.scrollTo(0, 0);
    };
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  // Verify credentials and preload lightweight stats, facets, and key aliases.
  useEffect(() => {
    if (!client) {
      setVerified(false);
      return;
    }
    const controller = new AbortController();
    setConnecting(true);
    setAuthError(null);

    Promise.all([
      client.getStats(365, controller.signal),
      client.getFacets(365, controller.signal).catch(() => [] as FacetCount[]),
      client.loadKeyAliases().catch(() => ({})),
    ])
      .then(([st, fc, aliases]) => {
        if (controller.signal.aborted) return;
        setStats(st);
        setFacets(fc);
        setKeyAliases(aliases);
        setVerified(true);
        setConnecting(false);
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        const msg =
          err instanceof ApiError
            ? `Connection failed (HTTP ${err.status}): ${err.message}`
            : err instanceof Error
              ? err.message
              : 'Connection failed';
        setAuthError(msg);
        setVerified(false);
        setConnecting(false);
        if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
          clearManualSession();
        }
      });

    return () => controller.abort();
  }, [client]);

  const navigate = useCallback((hash: string) => {
    if (window.location.hash !== hash) {
      window.location.hash = hash;
    } else {
      setRoute(parseHash(hash));
    }
  }, []);

  const handleManualLogin = (base: string, key: string) => {
    const next = saveManualSession(base, key);
    setSession(next);
  };

  const handleSignOut = () => {
    clearManualSession();
    setSession(null);
    setVerified(false);
    setStats(null);
  };

  const handleSelectModel = (model: string) => {
    setActiveFacets({ 'model.resolved': model });
    navigate('#/sessions');
  };

  const handleOpenSession = (s: ArchiveSession) => {
    setSelectedSession(s);
    navigate(`#/sessions/${encodeURIComponent(s.session_id)}`);
  };

  if (!session || (!verified && !connecting)) {
    return (
      <LoginView
        defaultBase={session?.base || window.location.origin}
        error={authError}
        loading={connecting}
        onLogin={handleManualLogin}
      />
    );
  }

  if (!verified && connecting) {
    return (
      <div className="min-h-screen bg-black text-zinc-400 flex items-center justify-center">
        <div className="flex items-center gap-2.5 text-sm">
          <span className="h-2.5 w-2.5 rounded-full bg-[#d97757] animate-pulse" />
          <span>Connecting to ClickHouse archive…</span>
        </div>
      </div>
    );
  }

  const activeTab = route.view === 'insights' ? 'insights' : 'sessions';

  return (
    <div className="min-h-screen bg-black text-zinc-100 flex flex-col">
      <TopNav
        activeTab={activeTab}
        stats={stats}
        session={session}
        onNavigate={navigate}
        onSignOut={handleSignOut}
      />

      <div className="flex-1">
        {client && route.view === 'insights' && (
          <InsightsView client={client} onSelectModel={handleSelectModel} />
        )}

        {client && route.view === 'sessions' && (
          <SessionsView
            client={client}
            facets={facets}
            keyAliases={keyAliases}
            activeFacets={activeFacets}
            onChangeFacets={setActiveFacets}
            onOpenSession={handleOpenSession}
          />
        )}

        {client && route.view === 'session' && (
          <SessionDetailView
            client={client}
            sessionId={route.sessionId}
            initialSession={
              selectedSession?.session_id === route.sessionId ? selectedSession : null
            }
            onBack={() => navigate('#/sessions')}
          />
        )}
      </div>
    </div>
  );
}
