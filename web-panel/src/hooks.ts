import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "./api";

// useApi loads a resource and reloads it on demand or periodically.
export function useApi<T>(path: string | null, intervalMs = 0) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(path !== null);
  const pathRef = useRef(path);
  pathRef.current = path;

  const reload = useCallback(async () => {
    const p = pathRef.current;
    if (!p) return;
    try {
      const d = await api.get<T>(p);
      if (pathRef.current === p) {
        setData(d);
        setError(null);
      }
    } catch (e) {
      if (pathRef.current === p) setError(e instanceof ApiError ? e : new ApiError(0, "network", String(e)));
    } finally {
      if (pathRef.current === p) setLoading(false);
    }
  }, []);

  useEffect(() => {
    setLoading(path !== null);
    void reload();
    if (!intervalMs || !path) return;
    const t = setInterval(() => void reload(), intervalMs);
    return () => clearInterval(t);
  }, [path, intervalMs, reload]);

  return { data, error, loading, reload, setData };
}
