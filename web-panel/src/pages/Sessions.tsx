import { History, Radio, Search, Users, XCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, qs } from "../api";
import { Panel } from "../components/panel";
import { Empty, ErrorBox, PageHeader } from "../components/ui";
import { bitrate, bytes, dateTime } from "../format";
import { useApi } from "../hooks";
import { useCan } from "../me";
import type { PlaySession } from "../types";

const PROTO: Record<string, string> = { hls: "HLS", mpegts: "MPEG-TS", dvr: "DVR" };
export const CLOSE_REASON: Record<string, string> = {
  idle: "parou de pedir",
  disconnected: "desconectou",
  killed: "encerrada pelo operador",
  replaced: "outra sessão do assinante",
  channel_stopped: "canal parado",
  backend_denied: "negada pelo backend",
  backend_unreachable: "backend fora do ar",
  ip_denied: "IP bloqueado",
  ip_not_allowed: "IP fora da lista",
};

const hhmm = (s: string) => new Date(s).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
const dur = (from: string, to?: string) => {
  const s = Math.max(
    0,
    Math.round(((to ? new Date(to).getTime() : Date.now()) - new Date(from).getTime()) / 1000),
  );
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h ? `${h} h ${m} min` : m ? `${m} min` : `${s} s`;
};
const rate = (s: PlaySession) => {
  const secs = (new Date(s.last_seen).getTime() - new Date(s.started_at).getTime()) / 1000;
  return secs > 5 ? (s.bytes * 8) / secs : 0;
};

const PERIODS = [
  { id: "1h", label: "1 hora", ms: 3600_000 },
  { id: "24h", label: "24 horas", ms: 86400_000 },
  { id: "7d", label: "7 dias", ms: 7 * 86400_000 },
  { id: "30d", label: "30 dias", ms: 30 * 86400_000 },
];

/** SessionHistory lists closed sessions kept 30 days (all channels or one). */
export function SessionHistory({ channelId }: { channelId?: string }) {
  const [period, setPeriod] = useState("24h");
  const [who, setWho] = useState("");
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<PlaySession[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  const load = async (more: boolean) => {
    setLoading(true);
    setError(null);
    const ms = PERIODS.find((p) => p.id === period)!.ms;
    const isIP = /^[0-9a-fA-F:.]+$/.test(query) && query.includes(query.includes(":") ? ":" : ".");
    try {
      const res = await api.get<{ items: PlaySession[]; next_cursor?: string }>(
        "/sessions/history" +
          qs({
            channel_id: channelId,
            from: new Date(Date.now() - ms).toISOString(),
            user_id: query && !isIP ? query : undefined,
            ip: query && isIP ? query : undefined,
            limit: 50,
            cursor: more ? cursor : undefined,
          }),
      );
      setItems((cur) => (more ? [...cur, ...res.items] : res.items));
      setCursor(res.next_cursor);
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reload on filter change
  }, [period, query, channelId]);

  return (
    <Panel
      icon={History}
      title="Histórico"
      sub="Sessões encerradas nos últimos 30 dias, em todos os servidores."
      flush
      actions={
        <div className="row" style={{ gap: 8 }}>
          <form
            className="search"
            onSubmit={(e) => {
              e.preventDefault();
              setQuery(who.trim());
            }}
          >
            <Search size={14} />
            <input
              value={who}
              placeholder="Assinante ou IP"
              onChange={(e) => setWho(e.target.value)}
              onBlur={() => setQuery(who.trim())}
              style={{ paddingLeft: 30, width: 180 }}
            />
          </form>
          <div className="seg">
            {PERIODS.map((p) => (
              <button key={p.id} className={period === p.id ? "on" : ""} onClick={() => setPeriod(p.id)}>
                {p.label}
              </button>
            ))}
          </div>
        </div>
      }
    >
      <ErrorBox error={error} />
      {items.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Espectador</th>
                {!channelId && <th>Canal</th>}
                <th>Protocolo</th>
                <th>Início</th>
                <th>Assistiu</th>
                <th>Dados</th>
                <th>Motivo do fim</th>
              </tr>
            </thead>
            <tbody>
              {items.map((s) => (
                <tr key={s.id}>
                  <td>
                    <div className="mono">{s.ip}</div>
                    {(s.user_id || s.user_agent) && (
                      <div className="small muted truncate" style={{ maxWidth: 260 }} title={s.user_agent}>
                        {s.user_id ? `assinante ${s.user_id}` : s.user_agent}
                      </div>
                    )}
                  </td>
                  {!channelId && (
                    <td>
                      <Link to={`/channels/${s.channel_id}?tab=sessions`}>{s.channel_id}</Link>
                    </td>
                  )}
                  <td>
                    <span className="tag">{PROTO[s.proto] ?? s.proto}</span>
                  </td>
                  <td title={dateTime(s.started_at)}>
                    {new Date(s.started_at).toLocaleString("pt-BR", {
                      dateStyle: "short",
                      timeStyle: "short",
                    })}
                  </td>
                  <td>{dur(s.started_at, s.last_seen)}</td>
                  <td>{bytes(s.bytes)}</td>
                  <td>
                    <span className={s.reason === "killed" ? "tag bad" : "small muted"}>
                      {CLOSE_REASON[s.reason ?? ""] ?? s.reason}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {cursor && (
            <div style={{ padding: 12, textAlign: "center" }}>
              <button className="secondary sm" disabled={loading} onClick={() => void load(true)}>
                {loading ? "Carregando…" : "Carregar mais"}
              </button>
            </div>
          )}
        </div>
      ) : (
        !loading && <Empty icon={History}>Nenhuma sessão encerrada neste período.</Empty>
      )}
    </Panel>
  );
}

/** Sessions: who is watching now on every server, and the history. */
export function Sessions() {
  const data = useApi<{ open: PlaySession[]; unreachable_workers?: string[] }>("/sessions", 5000);
  const canOperate = useCan("channels:operate");
  const [error, setError] = useState<unknown>(null);
  const open = data.data?.open ?? [];
  const total = open.reduce((n, s) => n + rate(s), 0);
  const users = new Set(open.map((s) => s.user_id || s.ip)).size;
  const channels = new Set(open.map((s) => s.channel_id)).size;
  const kill = async (s: PlaySession) => {
    if (!confirm(`Encerrar a sessão de ${s.user_id || s.ip}? Ela fica bloqueada por 2 minutos.`)) return;
    setError(null);
    try {
      await api.del(`/channels/${s.channel_id}/sessions/${s.id}`);
      void data.reload();
    } catch (e) {
      setError(e);
    }
  };
  return (
    <>
      <PageHeader title="Sessões" sub="Quem está assistindo, em todos os servidores · atualiza a cada 5 s" />
      <div className="stat-strip">
        {(
          [
            [Users, "Sessões abertas", open.length],
            [Users, "Espectadores únicos", users],
            [Radio, "Canais assistidos", channels],
            [Radio, "Banda estimada", total ? bitrate(total) : "—"],
          ] as const
        ).map(([Icon, label, value]) => (
          <div key={label} className="stat">
            <span className="stat-icon">
              <Icon size={16} />
            </span>
            <div>
              <div className="stat-label">{label}</div>
              <div className="stat-value">{value}</div>
            </div>
          </div>
        ))}
      </div>
      <ErrorBox error={error ?? data.error} />
      {!!data.data?.unreachable_workers?.length && (
        <div className="alert warn">Sem resposta de: {data.data.unreachable_workers.join(", ")}</div>
      )}
      <Panel icon={Users} title="Assistindo agora" flush>
        {open.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Espectador</th>
                  <th>Canal</th>
                  <th>Protocolo</th>
                  <th>Servidor</th>
                  <th>Início</th>
                  <th>Duração</th>
                  <th>Taxa</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {open.map((s) => (
                  <tr key={s.id}>
                    <td>
                      <div className="mono">{s.ip}</div>
                      {s.user_id && <div className="small muted">assinante {s.user_id}</div>}
                    </td>
                    <td>
                      <Link to={`/channels/${s.channel_id}?tab=sessions`}>{s.channel_id}</Link>
                    </td>
                    <td>
                      <span className="tag">{PROTO[s.proto] ?? s.proto}</span>
                    </td>
                    <td className="muted">{s.worker_id}</td>
                    <td>{hhmm(s.started_at)}</td>
                    <td>{dur(s.started_at)}</td>
                    <td>{bitrate(rate(s))}</td>
                    <td className="actions">
                      {canOperate && (
                        <button className="icon" title="Encerrar sessão" onClick={() => void kill(s)}>
                          <XCircle size={16} />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty icon={Users}>Ninguém assistindo agora.</Empty>
        )}
      </Panel>
      <SessionHistory />
    </>
  );
}
