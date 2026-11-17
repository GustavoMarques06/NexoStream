import { useEffect, useState } from "react";
import { api } from "./api";
import type { Channel, PlaybackToken } from "./types";

/** Tokens asked by the panel last 6 h and are renewed 15 min before they expire. */
const TTL_SECONDS = 6 * 3600;
const RENEW_BEFORE_MS = 15 * 60 * 1000;

const cache = new Map<string, Promise<PlaybackToken>>();

function fetchToken(id: string): Promise<PlaybackToken> {
  const hit = cache.get(id);
  if (hit) return hit;
  const p = api.post<PlaybackToken>(`/channels/${id}/playback-token`, { ttl_seconds: TTL_SECONDS });
  cache.set(id, p);
  p.then(
    (t) => {
      const ms = new Date(t.expires_at).getTime() - Date.now() - RENEW_BEFORE_MS;
      setTimeout(() => cache.delete(id), Math.max(ms, 60_000));
    },
    () => cache.delete(id),
  );
  return p;
}

/**
 * Playback token of a channel with DRM. For other channels it is ready at
 * once with no token. The token changes (and players reload) when renewed.
 */
export function usePlaybackToken(channel: Channel | null | undefined): {
  ready: boolean;
  token: string | null;
  expiresAt: string | null;
  error: unknown;
} {
  const id = channel?.id;
  const protectedCh = !!channel?.drm?.enabled;
  const [state, setState] = useState<{ id?: string; t?: PlaybackToken; error?: unknown }>({});

  useEffect(() => {
    if (!id || !protectedCh) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = () => {
      fetchToken(id).then(
        (t) => {
          if (!alive) return;
          setState({ id, t });
          const ms = new Date(t.expires_at).getTime() - Date.now() - RENEW_BEFORE_MS + 1000;
          timer = setTimeout(load, Math.max(ms, 60_000));
        },
        (error: unknown) => alive && setState({ id, error }),
      );
    };
    load();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [id, protectedCh]);

  if (!protectedCh) return { ready: true, token: null, expiresAt: null, error: null };
  const mine = state.id === id ? state : {};
  return {
    ready: !!mine.t,
    token: mine.t?.token ?? null,
    expiresAt: mine.t?.expires_at ?? null,
    error: mine.error ?? null,
  };
}

/** Appends the playback token to an output URL (no token: unchanged). */
export function withToken(url: string | undefined, token: string | null): string | undefined {
  if (!url || !token) return url;
  return url + (url.includes("?") ? "&" : "?") + "token=" + encodeURIComponent(token);
}
