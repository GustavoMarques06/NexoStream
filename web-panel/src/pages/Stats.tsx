import { Activity, BarChart3, Server, Trophy, Tv, Users } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { LineChart, type Series } from "../components/LineChart";
import { Panel } from "../components/panel";
import { Empty, ErrorBox, PageHeader } from "../components/ui";
import { bitrate } from "../format";
import { useApi } from "../hooks";

type Points = { t: string; v: number }[];
interface StatsResponse {
  from: string;
  to: string;
  step_seconds: number;
  series: Record<string, Points>;
}
interface TopResponse {
  items: { channel_id: string; name: string; viewer_minutes: number; peak_viewers: number }[];
}

const RANGES = [
  { id: "1h", label: "1 hora", ms: 3600_000 },
  { id: "24h", label: "24 horas", ms: 86400_000 },
  { id: "7d", label: "7 dias", ms: 7 * 86400_000 },
  { id: "30d", label: "30 dias", ms: 30 * 86400_000 },
];

const int = (v: number) => Math.round(v).toLocaleString("pt-BR");
const pct = (v: number) => `${Math.round(v)}%`;
const hours = (min: number) =>
  min >= 60
    ? `${(min / 60).toLocaleString("pt-BR", { maximumFractionDigits: 1 })} h`
    : `${Math.round(min)} min`;
const avg = (p?: Points) => (p?.length ? p.reduce((n, x) => n + x.v, 0) / p.length : 0);
const peak = (p?: Points) => (p?.length ? Math.max(...p.map((x) => x.v)) : 0);
const last = (p?: Points) => (p?.length ? p[p.length - 1]!.v : 0);

/** Statistics: audience and resource history (one sample per minute, 30 days). */
export function Stats() {
  const [range, setRange] = useState("24h");
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((x) => x + 1), 60000);
    return () => clearInterval(id);
  }, []);
  const ms = RANGES.find((r) => r.id === range)!.ms;
  // Rounded to the minute so the URL (and the request) is stable between renders.
  const { from, to } = useMemo(() => {
    const t = Math.floor(Date.now() / 60000) * 60000;
    return { from: new Date(t - ms).toISOString(), to: new Date(t).toISOString() };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- tick moves the window every minute
  }, [ms, tick]);
  const q = `from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`;
  const main = useApi<StatsResponse>(
    `/stats?keys=viewers.total,channels.up,channels.enabled,channels.failed,input.bps,workers.online&${q}`,
  );
  const workers = useApi<StatsResponse>(`/stats?prefix=worker:&${q}`);
  const top = useApi<TopResponse>(`/stats/top-channels?limit=10&${q}`);
  const s = main.data?.series ?? {};
  const f = new Date(from).getTime();
  const t = new Date(to).getTime();
  const one = (key: string, label: string): Series[] => [
    { key, label, color: "var(--accent-btn)", points: s[key] ?? [] },
  ];

  const byWorker = useMemo(() => {
    const out: Record<string, Record<string, Points>> = {};
    for (const [k, pts] of Object.entries(workers.data?.series ?? {})) {
      const [, id, metric] = k.split(":");
      if (!id || !metric) continue;
      (out[id] ??= {})[metric] = pts;
    }
    return out;
  }, [workers.data]);

  const tiles: [typeof Users, string, string, string][] = [
    [
      Users,
      "Espectadores agora",
      int(last(s["viewers.total"])),
      `pico ${int(peak(s["viewers.total"]))} no período`,
    ],
    [Tv, "Canais no ar", int(last(s["channels.up"])), `de ${int(last(s["channels.enabled"]))} ativos`],
    [Activity, "Entrada total", bitrate(last(s["input.bps"])), `média ${bitrate(avg(s["input.bps"]))}`],
    [
      Server,
      "Servidores online",
      int(last(s["workers.online"])),
      `${Object.keys(byWorker).length} com dados no período`,
    ],
  ];

  return (
    <>
      <PageHeader
        title="Estatísticas"
        sub="Audiência e recursos ao longo do tempo · uma amostra por minuto, guardada por 30 dias"
      >
        <div className="seg">
          {RANGES.map((r) => (
            <button key={r.id} className={range === r.id ? "on" : ""} onClick={() => setRange(r.id)}>
              {r.label}
            </button>
          ))}
        </div>
      </PageHeader>
      <ErrorBox error={main.error ?? workers.error ?? top.error} />

      <div className="stat-strip">
        {tiles.map(([Icon, label, value, sub]) => (
          <div key={label} className="stat">
            <span className="stat-icon">
              <Icon size={16} />
            </span>
            <div style={{ minWidth: 0 }}>
              <div className="stat-label">{label}</div>
              <div className="stat-value">{main.data ? value : "—"}</div>
              <div className="stat-label">{main.data ? sub : ""}</div>
            </div>
          </div>
        ))}
      </div>

      <div className="stats-grid">
        <Panel icon={Users} title="Espectadores simultâneos" sub="Sessões abertas em todos os servidores.">
          <LineChart series={one("viewers.total", "Espectadores")} format={int} integer from={f} to={t} />
        </Panel>
        <Panel icon={Tv} title="Canais no ar" sub="Canais entregando sinal (no ar ou no backup).">
          <LineChart series={one("channels.up", "Canais no ar")} format={int} integer from={f} to={t} />
        </Panel>
        <Panel
          icon={Activity}
          title="Taxa de entrada total"
          sub="Soma do que os canais no ar estão recebendo."
        >
          <LineChart series={one("input.bps", "Entrada")} format={(v) => bitrate(v)} from={f} to={t} />
        </Panel>
        <Panel icon={Trophy} title="Canais mais assistidos" sub="Em espectador-hora no período." flush>
          {top.data?.items.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>#</th>
                    <th>Canal</th>
                    <th>Audiência</th>
                    <th>Pico</th>
                  </tr>
                </thead>
                <tbody>
                  {top.data.items.map((x, i) => (
                    <tr key={x.channel_id}>
                      <td className="muted">{i + 1}</td>
                      <td>
                        <Link to={`/channels/${x.channel_id}`}>{x.name}</Link>
                      </td>
                      <td>{hours(x.viewer_minutes)}</td>
                      <td>{int(x.peak_viewers)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty icon={Trophy}>Ninguém assistiu no período.</Empty>
          )}
        </Panel>
      </div>

      <h2 className="section-title">
        <BarChart3 size={16} /> Servidores
      </h2>
      {Object.keys(byWorker).length ? (
        <div className="stats-grid">
          {Object.entries(byWorker)
            .sort(([a], [b]) => a.localeCompare(b))
            .map(([id, m]) => (
              <Panel
                key={id}
                icon={Server}
                title={id}
                sub={`CPU média ${pct(avg(m.cpu))} · pico ${pct(peak(m.cpu))}`}
              >
                <LineChart
                  series={[
                    { key: "cpu", label: "CPU", color: "var(--series-1)", points: m.cpu ?? [] },
                    { key: "mem", label: "Memória", color: "var(--series-2)", points: m.mem ?? [] },
                  ]}
                  format={pct}
                  max={100}
                  height={170}
                  from={f}
                  to={t}
                />
              </Panel>
            ))}
        </div>
      ) : (
        <Panel icon={Server} title="Servidores">
          <Empty icon={Server}>Sem dados de servidores no período.</Empty>
        </Panel>
      )}
    </>
  );
}
