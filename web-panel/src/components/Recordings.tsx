import { Archive, History } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { bytes } from "../format";
import type { Recordings as Rec } from "../types";

const WINDOWS = [
  { label: "6 h", hours: 6 },
  { label: "24 h", hours: 24 },
  { label: "3 d", hours: 72 },
];

const JUMPS = [
  { label: "−5 min", min: 5 },
  { label: "−30 min", min: 30 },
  { label: "−1 h", min: 60 },
  { label: "−6 h", min: 360 },
];

function toLocalInput(d: Date) {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/**
 * Recordings shows the recorded ranges of a channel (read directly from the
 * Media Node that records it) and starts timeshift playback from a moment.
 */
export function Recordings({
  base,
  retentionHours,
  playingFrom,
  onPlay,
}: {
  base: string; // <node>/dvr/<channel>/
  retentionHours: number;
  playingFrom: number | null; // unix seconds, null = live
  onPlay: (fromUnix: number) => void;
}) {
  const [rec, setRec] = useState<Rec | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [hours, setHours] = useState(Math.min(24, retentionHours || 24));
  const [hover, setHover] = useState<number | null>(null);
  const [at, setAt] = useState(toLocalInput(new Date(Date.now() - 30 * 60_000)));
  const bar = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let stop = false;
    const load = async () => {
      try {
        const from = Math.floor(Date.now() / 1000) - hours * 3600;
        const res = await fetch(`${base}recordings.json?from=${from}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = (await res.json()) as Rec;
        if (!stop) {
          setRec(data);
          setError(null);
        }
      } catch (e) {
        if (!stop) setError(String(e));
      }
    };
    void load();
    const t = setInterval(() => void load(), 30_000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [base, hours]);

  const now = Date.now();
  const start = now - hours * 3600_000;
  const pos = (t: number) => ((t - start) / (now - start)) * 100;
  const atPointer = (clientX: number) => {
    const r = bar.current?.getBoundingClientRect();
    if (!r) return now;
    return start + ((clientX - r.left) / r.width) * (now - start);
  };
  const firstRecorded = rec?.ranges[0] ? new Date(rec.ranges[0].from).getTime() : null;

  return (
    <section className="card">
      <div className="card-head">
        <h2>
          <Archive size={16} /> Gravações
        </h2>
        <div className="spacer" />
        {rec && (
          <span className="small muted">
            {rec.segments} segmentos · {bytes(rec.bytes)}
          </span>
        )}
        <div className="segmented">
          {WINDOWS.filter((w) => w.hours <= Math.max(retentionHours, 6)).map((w) => (
            <button key={w.hours} className={hours === w.hours ? "on" : ""} onClick={() => setHours(w.hours)}>
              {w.label}
            </button>
          ))}
        </div>
      </div>
      {error && <div className="muted small">Não foi possível ler as gravações do servidor: {error}</div>}
      <div
        ref={bar}
        className="rec-timeline"
        onMouseMove={(e) => setHover(atPointer(e.clientX))}
        onMouseLeave={() => setHover(null)}
        onClick={(e) => onPlay(Math.floor(atPointer(e.clientX) / 1000))}
        title={
          hover ? new Date(hover).toLocaleString("pt-BR") : "Clique para assistir a partir deste horário"
        }
      >
        {rec?.ranges.map((r, i) => {
          const a = Math.max(0, pos(new Date(r.from).getTime()));
          const b = Math.min(100, pos(new Date(r.to).getTime()));
          return b > a ? (
            <span key={i} className="rec-range" style={{ left: `${a}%`, width: `${b - a}%` }} />
          ) : null;
        })}
        {playingFrom !== null && (
          <span className="rec-cursor" style={{ left: `${pos(playingFrom * 1000)}%` }} />
        )}
        {hover !== null && <span className="rec-hover" style={{ left: `${pos(hover)}%` }} />}
      </div>
      <div className="rec-axis">
        <span>
          {new Date(start).toLocaleString("pt-BR", {
            day: "2-digit",
            month: "2-digit",
            hour: "2-digit",
            minute: "2-digit",
          })}
        </span>
        <span>agora</span>
      </div>
      <div className="rec-actions">
        <History size={15} className="muted" />
        {JUMPS.map((j) => (
          <button
            key={j.min}
            className="secondary sm"
            disabled={firstRecorded !== null && now - j.min * 60_000 < firstRecorded}
            onClick={() => onPlay(Math.floor((now - j.min * 60_000) / 1000))}
          >
            {j.label}
          </button>
        ))}
        <div className="spacer" />
        <input
          type="datetime-local"
          value={at}
          onChange={(e) => setAt(e.target.value)}
          aria-label="Assistir a partir de"
        />
        <button className="sm" onClick={() => onPlay(Math.floor(new Date(at).getTime() / 1000))}>
          Assistir
        </button>
      </div>
    </section>
  );
}
