export interface AuthSession {
  base: string;
  key: string;
  source: 'cpamp' | 'manual';
}

const STORAGE_KEY_AUTH = 'cli-proxy-auth';
const SESSION_KEY_MANUAL = 'cpa-archive-manual-session';
const SECRET_SALT = 'cli-proxy-api-webui::secure-storage';
const ENC_PREFIX_V1 = 'enc::v1::';
const ENC_PREFIX_V2 = 'enc::v2::';

function xorBytes(data: Uint8Array, keyBytes: Uint8Array): Uint8Array {
  const out = new Uint8Array(data.length);
  for (let i = 0; i < data.length; i++) {
    out[i] = data[i] ^ keyBytes[i % keyBytes.length];
  }
  return out;
}

function fromBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    bytes[i] = bin.charCodeAt(i);
  }
  return bytes;
}

export function decodeCpampPayload(raw: string, host: string, userAgent: string): string {
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();

  if (raw.startsWith(ENC_PREFIX_V2)) {
    const encrypted = fromBase64(raw.slice(ENC_PREFIX_V2.length));
    const keyBytes = encoder.encode(`${SECRET_SALT}|v2|${host}`);
    return decoder.decode(xorBytes(encrypted, keyBytes));
  }

  if (raw.startsWith(ENC_PREFIX_V1)) {
    const encrypted = fromBase64(raw.slice(ENC_PREFIX_V1.length));
    const keyBytes = encoder.encode(`${SECRET_SALT}|${host}|${userAgent}`);
    return decoder.decode(xorBytes(encrypted, keyBytes));
  }

  return raw;
}

function parseCpampStorage(storage: Storage, host: string, userAgent: string): AuthSession | null {
  try {
    const raw = storage.getItem(STORAGE_KEY_AUTH);
    if (!raw) return null;

    const decoded = decodeCpampPayload(raw, host, userAgent);
    const parsed = JSON.parse(decoded) as {
      state?: { apiBase?: string; managementKey?: string };
      apiBase?: string;
      managementKey?: string;
    };
    const state = parsed.state ?? parsed;
    const key = typeof state.managementKey === 'string' ? state.managementKey.trim() : '';
    if (!key) return null;

    const rawBase = typeof state.apiBase === 'string' ? state.apiBase.trim() : '';
    const base = (rawBase || window.location.origin).replace(/\/$/, '');
    return { base, key, source: 'cpamp' };
  } catch {
    return null;
  }
}

export function readAutoAuth(): AuthSession | null {
  // 1. Prefer CPAMP auth stored on same-origin localStorage (supports enc::v2::, enc::v1::, and JSON).
  const direct = parseCpampStorage(window.localStorage, window.location.host, navigator.userAgent);
  if (direct) return direct;

  // 2. Fallback to parent window localStorage if embedded on same origin.
  try {
    if (window.parent && window.parent !== window && window.parent.localStorage) {
      const parentAuth = parseCpampStorage(
        window.parent.localStorage,
        window.parent.location.host,
        navigator.userAgent
      );
      if (parentAuth) return parentAuth;
    }
  } catch {
    // Cross-origin parent; ignore.
  }

  // 3. Tab-scoped standalone manual login (sessionStorage only).
  try {
    const manual = window.sessionStorage.getItem(SESSION_KEY_MANUAL);
    if (manual) {
      const parsed = JSON.parse(manual) as { base?: string; key?: string };
      if (parsed.base && parsed.key) {
        return { base: parsed.base.replace(/\/$/, ''), key: parsed.key, source: 'manual' };
      }
    }
  } catch {
    // Ignore storage errors.
  }

  return null;
}

export function saveManualSession(base: string, key: string): AuthSession {
  const session: AuthSession = {
    base: base.trim().replace(/\/$/, ''),
    key: key.trim(),
    source: 'manual',
  };
  try {
    window.sessionStorage.setItem(
      SESSION_KEY_MANUAL,
      JSON.stringify({ base: session.base, key: session.key })
    );
  } catch {
    // Ignore storage errors in restricted environments.
  }
  return session;
}

export function clearManualSession(): void {
  try {
    window.sessionStorage.removeItem(SESSION_KEY_MANUAL);
  } catch {
    // Ignore.
  }
}

export function isEmbeddedInIframe(): boolean {
  try {
    return window.self !== window.top;
  } catch {
    return true;
  }
}

export function standaloneUrl(): string {
  const hash = window.location.hash || '#/insights';
  // If already served via CPAMP's plugin resource route, open the same path standalone (or /archive if available).
  return `${window.location.origin}${window.location.pathname}${hash}`;
}
