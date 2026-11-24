import { useState, type FormEvent } from 'react';
import { Database, KeyRound, ArrowRight, AlertCircle } from 'lucide-react';

interface LoginViewProps {
  defaultBase: string;
  error: string | null;
  loading: boolean;
  onLogin: (base: string, key: string) => void;
}

export function LoginView({ defaultBase, error, loading, onLogin }: LoginViewProps) {
  const [base, setBase] = useState(defaultBase);
  const [key, setKey] = useState('');

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!base.trim() || !key.trim()) return;
    onLogin(base, key);
  };

  return (
    <div className="min-h-screen bg-black text-zinc-100 flex items-center justify-center p-4">
      <div className="w-full max-w-md rounded-xl border border-zinc-800/90 bg-[#0a0a0a] p-7 shadow-2xl shadow-black">
        <div className="flex items-center gap-2.5 text-zinc-400 text-xs uppercase tracking-widest mb-4">
          <Database className="w-3.5 h-3.5 text-[#d97757]" />
          <span>Session Archive · ClickHouse</span>
        </div>

        <h1 className="text-xl font-semibold tracking-tight text-white">Connect to CPAMP</h1>
        <p className="mt-1.5 text-sm text-zinc-400 leading-relaxed">
          Signed into CPAMP on this origin? Auto-auth picks up your session automatically. Otherwise
          enter your panel URL and admin key (stored in this tab only).
        </p>

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <label className="block">
            <span className="block text-xs font-medium text-zinc-400 mb-1.5">API Base URL</span>
            <input
              type="url"
              value={base}
              onChange={(e) => setBase(e.target.value)}
              placeholder="https://cpamp.planasolutions.ai"
              autoComplete="url"
              spellCheck={false}
              required
              className="w-full rounded-lg border border-zinc-800 bg-black px-3.5 py-2.5 text-sm text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-500 focus:outline-none transition-colors"
            />
          </label>

          <label className="block">
            <span className="block text-xs font-medium text-zinc-400 mb-1.5">
              Management / Admin Key
            </span>
            <div className="relative">
              <KeyRound className="w-4 h-4 text-zinc-500 absolute left-3.5 top-1/2 -translate-y-1/2 pointer-events-none" />
              <input
                type="password"
                value={key}
                onChange={(e) => setKey(e.target.value)}
                placeholder="Enter admin key"
                autoComplete="current-password"
                required
                className="w-full rounded-lg border border-zinc-800 bg-black pl-10 pr-3.5 py-2.5 text-sm text-zinc-100 placeholder:text-zinc-600 focus:border-zinc-500 focus:outline-none transition-colors"
              />
            </div>
          </label>

          {error && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-red-500/30 bg-red-950/30 px-3 py-2.5 text-xs text-red-300"
            >
              <AlertCircle className="w-4 h-4 shrink-0 mt-0.5 text-red-400" />
              <span>{error}</span>
            </div>
          )}

          <button
            type="submit"
            disabled={loading}
            className="w-full inline-flex items-center justify-center gap-2 rounded-lg bg-zinc-100 px-4 py-2.5 text-sm font-medium text-black hover:bg-white disabled:opacity-50 transition-colors cursor-pointer"
          >
            <span>{loading ? 'Connecting…' : 'Sign in'}</span>
            <ArrowRight className="w-4 h-4" />
          </button>
        </form>
      </div>
    </div>
  );
}
