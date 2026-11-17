import { useMemo, useRef, useState } from "react";

export interface Series {
  key: string;
  label: string;
  /** CSS color (a --series-N or --accent token). */
  color: string;
  points: { t: string; v: number }[];
}

const W = 640;
const PAD = { l: 52, r: 86, t: 12, b: 26 };

function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

const timeLabel = (ms: number, span: number) => {
  const d = new Date(ms);
  return span > 2 * 86400_000
    ? d.toLocaleDateString("pt-BR", { day: "2-digit", month: "2-digit" })
    : d.toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
};

/**
 * LineChart: thin lines on one y axis, recessive grid, crosshair tooltip,
 * legend for 2+ series plus direct end labels, and a table view.
 */
export function LineChart({
  series,
  format,
  height = 190,
  max: fixedMax,
  integer,
  from,
  to,
}: {
  series: Series[];
  format: (v: number) => string;
  height?: number;
  /** Fixed top of the y axis (e.g. 100 for percent). */
  max?: number;
  /** Counts: the y axis uses whole numbers only. */
  integer?: boolean;
  from: number;
  to: number;
}) {
  const H = height;
  const svg = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const [table, setTable] = useState(false);
  const iw = W - PAD.l - PAD.r;
  const ih = H - PAD.t - PAD.b;
  const span = Math.max(1, to - from);
  const dataMax = Math.max(0, ...series.flatMap((s) => s.points.map((p) => p.v)));
  const top =
    fixedMax ?? (integer ? Math.max(1, Math.ceil((dataMax * 1.1) / 4)) * 4 : niceMax(dataMax * 1.1));
  const x = (ms: number) => PAD.l + ((ms - from) / span) * iw;
  const y = (v: number) => PAD.t + ih - (Math.min(v, top) / top) * ih;
  const times = useMemo(
    () =>
      [...new Set(series.flatMap((s) => s.points.map((p) => new Date(p.t).getTime())))].sort((a, b) => a - b),
    [series],
  );
  const empty = times.length === 0;

  const path = (s: Series) => {
    let d = "";
    let prev = 0;
    for (const p of s.points) {
      const ms = new Date(p.t).getTime();
      // A gap longer than 3 buckets breaks the line (no data, not zero).
      const gap =
        prev &&
        times.length > 1 &&
        ms - prev > 3 * ((times[times.length - 1]! - times[0]!) / Math.max(1, times.length - 1));
      d += `${d && !gap ? "L" : "M"}${x(ms).toFixed(1)},${y(p.v).toFixed(1)}`;
      prev = ms;
    }
    return d;
  };

  const onMove = (e: React.MouseEvent) => {
    const r = svg.current?.getBoundingClientRect();
    if (!r || empty) return;
    const ms = from + ((((e.clientX - r.left) / r.width) * W - PAD.l) / iw) * span;
    let best = times[0]!;
    for (const t of times) if (Math.abs(t - ms) < Math.abs(best - ms)) best = t;
    setHover(best);
  };
  const valueAt = (s: Series, ms: number) => s.points.find((p) => new Date(p.t).getTime() === ms)?.v;

  // Direct end labels (2-4 series), pushed apart so they never overlap.
  const ends =
    series.length >= 2 && series.length <= 4
      ? series
          .map((s) => ({ s, last: s.points[s.points.length - 1] }))
          .filter((e) => e.last)
          .map((e) => ({ ...e, yy: y(e.last!.v) }))
          .sort((a, b) => a.yy - b.yy)
      : [];
  for (let i = 1; i < ends.length; i++)
    if (ends[i]!.yy - ends[i - 1]!.yy < 14) ends[i]!.yy = ends[i - 1]!.yy + 14;

  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * top);
  const xticks = [0, 0.25, 0.5, 0.75, 1].map((f) => from + f * span);
  const hx = hover !== null ? x(hover) : 0;

  return (
    <div className="chart">
      {series.length >= 2 && (
        <div className="chart-legend">
          {series.map((s) => (
            <span key={s.key}>
              <i style={{ background: s.color }} /> {s.label}
            </span>
          ))}
        </div>
      )}
      <div className="chart-plot">
        <svg
          ref={svg}
          viewBox={`0 0 ${W} ${H}`}
          role="img"
          aria-label={series.map((s) => s.label).join(", ")}
          onMouseMove={onMove}
          onMouseLeave={() => setHover(null)}
        >
          {ticks.map((v) => (
            <g key={v}>
              <line className="chart-grid" x1={PAD.l} x2={W - PAD.r} y1={y(v)} y2={y(v)} />
              <text className="chart-axis" x={PAD.l - 8} y={y(v) + 4} textAnchor="end">
                {format(v)}
              </text>
            </g>
          ))}
          {xticks.map((t, i) => (
            <text
              key={t}
              className="chart-axis"
              x={x(t)}
              y={H - 6}
              textAnchor={i === 0 ? "start" : i === 4 ? "end" : "middle"}
            >
              {timeLabel(t, span)}
            </text>
          ))}
          {series.length === 1 && series[0]!.points.length > 1 && (
            <path
              className="chart-area"
              d={`${path(series[0]!)}L${x(new Date(series[0]!.points[series[0]!.points.length - 1]!.t).getTime()).toFixed(1)},${y(0)}L${x(new Date(series[0]!.points[0]!.t).getTime()).toFixed(1)},${y(0)}Z`}
              style={{ fill: series[0]!.color }}
            />
          )}
          {series.map((s) => (
            <g key={s.key}>
              <path className="chart-line" d={path(s)} style={{ stroke: s.color }} />
              {/* Few samples (just after start): show them as markers. */}
              {s.points.length < 4 &&
                s.points.map((p) => (
                  <circle
                    key={p.t}
                    className="chart-dot"
                    cx={x(new Date(p.t).getTime())}
                    cy={y(p.v)}
                    r={4}
                    style={{ fill: s.color }}
                  />
                ))}
            </g>
          ))}
          {ends.map((e) => (
            <text key={e.s.key} className="chart-end" x={W - PAD.r + 8} y={e.yy + 4}>
              {e.s.label}
            </text>
          ))}
          {hover !== null && (
            <g>
              <line className="chart-cross" x1={hx} x2={hx} y1={PAD.t} y2={PAD.t + ih} />
              {series.map((s) => {
                const v = valueAt(s, hover);
                return v === undefined ? null : (
                  <circle
                    key={s.key}
                    className="chart-dot"
                    cx={hx}
                    cy={y(v)}
                    r={4}
                    style={{ fill: s.color }}
                  />
                );
              })}
            </g>
          )}
          <rect x={PAD.l} y={PAD.t} width={iw} height={ih} fill="transparent" />
        </svg>
        {hover !== null && (
          <div className="chart-tip" style={{ left: `${(hx / W) * 100}%` }}>
            <div className="chart-tip-time">
              {new Date(hover).toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" })}
            </div>
            {series.map((s) => {
              const v = valueAt(s, hover);
              return (
                <div key={s.key} className="chart-tip-row">
                  <i style={{ background: s.color }} /> {s.label}
                  <b>{v === undefined ? "—" : format(v)}</b>
                </div>
              );
            })}
          </div>
        )}
        {empty && <div className="chart-empty">Sem dados neste período.</div>}
      </div>
      {!empty && (
        <button className="link chart-toggle" onClick={() => setTable(!table)}>
          {table ? "Ocultar tabela" : "Ver tabela"}
        </button>
      )}
      {table && (
        <div className="chart-table">
          <table>
            <thead>
              <tr>
                <th>Horário</th>
                {series.map((s) => (
                  <th key={s.key}>{s.label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {[...times].reverse().map((t) => (
                <tr key={t}>
                  <td>{new Date(t).toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" })}</td>
                  {series.map((s) => {
                    const v = valueAt(s, t);
                    return <td key={s.key}>{v === undefined ? "—" : format(v)}</td>;
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
