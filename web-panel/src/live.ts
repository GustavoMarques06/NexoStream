// One shared Server-Sent Events connection for the whole panel. It keeps the
// latest status of every channel and a short bitrate history for sparklines,
// and lets pages react to new events.
import { useEffect, useRef, useSyncExternalStore } from "react";
import { authHeader, isLoggedIn } from "./api";
import type { ChannelStatus, NsEvent } from "./types";

const HISTORY = 48; // samples (~4 min at one status every 5 s)

const statuses = new Map<string, ChannelStatus>();
const history = new Map<string, number[]>();
const eventListeners = new Set<(e: NsEvent) => void>();
const listeners = new Set<() => void>();
let version = 0;
let connected = false;
let started = false;
let pending: ReturnType<typeof setTimeout> | null = null;

function notify() {
  if (pending) return;
  pending = setTimeout(() => {
    pending = null;
    version++;
    listeners.forEach((l) => l());
  }, 250);
}

function pushStatus(st: ChannelStatus) {
  statuses.set(st.channel_id, st);
  const h = history.get(st.channel_id) ?? [];
  h.push(st.input?.bitrate_bps ?? 0);
  if (h.length > HISTORY) h.splice(0, h.length - HISTORY);
  history.set(st.channel_id, h);
}

/** seed merges statuses loaded through the REST API. */
export function seed(list: ChannelStatus[]) {
  let changed = false;
  for (const st of list) {
    const cur = statuses.get(st.channel_id);
    if (!cur || (st.updated_at ?? "") > (cur.updated_at ?? "")) {
      pushStatus(st);
      changed = true;
    }
  }
  if (changed) notify();
}

async function loop() {
  for (;;) {
    if (!isLoggedIn()) {
      await new Promise((r) => setTimeout(r, 2000));
      continue;
    }
    try {
      const res = await fetch("/api/v1/stream/events?status=true", { headers: await authHeader() });
      if (!res.ok || !res.body) throw new Error("stream " + res.status);
      connected = true;
      notify();
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i: number;
        while ((i = buf.indexOf("\n\n")) >= 0) {
          const block = buf.slice(0, i);
          buf = buf.slice(i + 2);
          let ev = "";
          let data = "";
          for (const line of block.split("\n")) {
            if (line.startsWith("event: ")) ev = line.slice(7);
            else if (line.startsWith("data: ")) data += line.slice(6);
          }
          if (!data) continue;
          try {
            const parsed: unknown = JSON.parse(data);
            if (ev === "channel.status") pushStatus(parsed as ChannelStatus);
            else eventListeners.forEach((l) => l(parsed as NsEvent));
            notify();
          } catch {
            // ignore malformed message
          }
        }
      }
    } catch {
      // reconnect below
    }
    connected = false;
    notify();
    await new Promise((r) => setTimeout(r, 3000));
  }
}

export function startLive() {
  if (started) return;
  started = true;
  void loop();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** useLive re-renders the component when live data changes. */
export function useLive() {
  useSyncExternalStore(subscribe, () => version);
  return {
    connected,
    status: (id: string) => statuses.get(id),
    history: (id: string) => history.get(id) ?? [],
  };
}

/** useLiveEvents calls fn for every channel/worker event. */
export function useLiveEvents(fn: (e: NsEvent) => void) {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    const l = (e: NsEvent) => ref.current(e);
    eventListeners.add(l);
    return () => {
      eventListeners.delete(l);
    };
  }, []);
}
