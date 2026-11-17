// HTTP client for /api/v1 with access-token renewal.

const BASE = "/api/v1";
const STORAGE_KEY = "nexostream.session";

interface Session {
  access_token: string;
  refresh_token: string;
  expires_at: number; // epoch ms
}

let session: Session | null = load();
let refreshing: Promise<boolean> | null = null;
const listeners = new Set<() => void>();

function load(): Session | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    return raw ? (JSON.parse(raw) as Session) : null;
  } catch {
    return null;
  }
}

function save(s: Session | null) {
  session = s;
  try {
    if (s) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(s));
    else sessionStorage.removeItem(STORAGE_KEY);
  } catch {
    // storage unavailable: keep the session in memory only
  }
  listeners.forEach((l) => l());
}

export function onSessionChange(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function isLoggedIn(): boolean {
  return session !== null;
}

export class ApiError extends Error {
  status: number;
  code: string;
  details?: unknown;
  requestId?: string;
  constructor(status: number, code: string, message: string, details?: unknown, requestId?: string) {
    super(message);
    this.status = status;
    this.code = code;
    this.details = details;
    this.requestId = requestId;
  }
}

interface TokenResponse {
  access_token: string;
  refresh_token: string;
  expires_in: number;
}

function store(t: TokenResponse) {
  save({
    access_token: t.access_token,
    refresh_token: t.refresh_token,
    expires_at: Date.now() + t.expires_in * 1000,
  });
}

export async function login(username: string, password: string): Promise<void> {
  const res = await fetch(`${BASE}/auth/token`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) throw await toError(res);
  store((await res.json()) as TokenResponse);
}

export function logout() {
  // Revoke the tokens on the server too (best effort), then forget them.
  const token = session?.access_token;
  if (token) {
    void fetch(`${BASE}/auth/logout`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
    }).catch(() => undefined);
  }
  save(null);
}

async function refresh(): Promise<boolean> {
  if (!session) return false;
  if (!refreshing) {
    const rt = session.refresh_token;
    refreshing = (async () => {
      try {
        const res = await fetch(`${BASE}/auth/refresh`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ refresh_token: rt }),
        });
        if (!res.ok) {
          save(null);
          return false;
        }
        store((await res.json()) as TokenResponse);
        return true;
      } catch {
        return false;
      } finally {
        refreshing = null;
      }
    })();
  }
  return refreshing;
}

async function toError(res: Response): Promise<ApiError> {
  try {
    const body = (await res.json()) as {
      error?: { code: string; message: string; details?: unknown; request_id?: string };
    };
    if (body.error)
      return new ApiError(
        res.status,
        body.error.code,
        body.error.message,
        body.error.details,
        body.error.request_id,
      );
  } catch {
    // not JSON
  }
  return new ApiError(res.status, "http_" + res.status, `Erro HTTP ${res.status}`);
}

export async function authHeader(): Promise<Record<string, string>> {
  if (session && session.expires_at - Date.now() < 30_000) await refresh();
  return session ? { Authorization: `Bearer ${session.access_token}` } : {};
}

export async function request<T>(method: string, path: string, body?: unknown, retry = true): Promise<T> {
  const headers: Record<string, string> = { ...(await authHeader()) };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401 && retry && session) {
    if (await refresh()) return request<T>(method, path, body, false);
  }
  if (!res.ok) throw await toError(res);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/** getText fetches a text/plain resource (config export). */
export async function getText(path: string): Promise<string> {
  const res = await fetch(BASE + path, { headers: await authHeader() });
  if (!res.ok) throw await toError(res);
  return res.text();
}

export const api = {
  get: <T>(p: string) => request<T>("GET", p),
  post: <T>(p: string, b?: unknown) => request<T>("POST", p, b ?? {}),
  patch: <T>(p: string, b: unknown) => request<T>("PATCH", p, b),
  put: <T>(p: string, b: unknown) => request<T>("PUT", p, b),
  del: <T>(p: string) => request<T>("DELETE", p),
};

export function qs(params: Record<string, string | number | boolean | undefined | null>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params))
    if (v !== undefined && v !== null && v !== "") u.set(k, String(v));
  const s = u.toString();
  return s ? "?" + s : "";
}
