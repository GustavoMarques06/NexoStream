import { Activity, AlertTriangle, Gauge, Server, ShieldCheck, Tv } from "lucide-react";
import { Link } from "react-router-dom";
import { Empty, ErrorBox, eventLook, Kpi, Meter, PageHeader, StateChip, WorkerChip } from "../components/ui";
import { ago, bitrate, describeEvent, EVENT_LABEL, reason, STATE_LABEL } from "../format";
import { useApi } from "../hooks";
import { useLiveEvents } from "../live";
import { STATES, type ChannelState, type Summary } from "../types";

const STATE_COLOR: Record<ChannelState, string> = {
  RUNNING: "var(--ok)",
  STARTING: "var(--info)",
  DEGRADED: "var(--warn)",
  SWITCHING: "var(--warn)",
  ON_BACKUP: "var(--warn)",
  RECOVERING: "var(--warn)",
  FAILED: "var(--bad)",
  STOPPED: "var(--idle)",
};

export function Dashboard() {
  const { data, error, reload } = useApi<Summary>("/system/summary", 5000);
  useLiveEvents(() => void reload());

  if (error) return <ErrorBox error={error} />;
  if (!data) return null;
  const c = data.channels;
  const n = (s: ChannelState) => c.by_state[s] ?? 0;
  const up = n("RUNNING") + n("ON_BACKUP") + n("RECOVERING");
  const online = (data.workers.by_state.ONLINE ?? 0) + (data.workers.by_state.DRAINING ?? 0);
  const total = Math.max(c.total, 1);

  return (
    <>
      <PageHeader title="Visão geral" sub={`Atualizado ${ago(data.generated_at)}`} />

      <section className="grid grid-kpi">
        <Kpi
          icon={Tv}
          label="Canais no ar"
          tone="ok"
          value={
            <>
              {up} <small>/ {c.total}</small>
            </>
          }
        />
        <Kpi
          icon={AlertTriangle}
          label="Precisam de atenção"
          tone={data.failing.length ? "bad" : "ok"}
          value={data.failing.length}
        />
        <Kpi icon={Gauge} label="Entrada total" value={bitrate(c.input_bitrate_bps)} />
        <Kpi
          icon={Server}
          label="Servidores online"
          tone={online < data.workers.total ? "bad" : "ok"}
          value={
            <>
              {online} <small>/ {data.workers.total}</small>
            </>
          }
        />
      </section>

      <section className="card" style={{ marginBottom: 16 }}>
        <div className="card-head">
          <h2>Estado dos canais</h2>
        </div>
        <div className="dist">
          {STATES.filter((s) => n(s) > 0).map((s) => (
            <span
              key={s}
              style={{ width: `${(n(s) / total) * 100}%`, background: STATE_COLOR[s] }}
              title={`${STATE_LABEL[s]}: ${n(s)}`}
            />
          ))}
        </div>
        <div className="legend">
          {STATES.filter((s) => n(s) > 0).map((s) => (
            <Link key={s} to={`/channels?state=${s}`}>
              <span className="dot" style={{ background: STATE_COLOR[s] }} />
              {STATE_LABEL[s]} <strong>{n(s)}</strong>
            </Link>
          ))}
          {c.total === 0 && <span className="muted">Nenhum canal cadastrado</span>}
        </div>
      </section>

      <div className="grid grid-2">
        <section className="card">
          <div className="card-head">
            <h2>
              <AlertTriangle size={16} /> Atenção
            </h2>
          </div>
          {data.failing.length === 0 ? (
            <Empty icon={ShieldCheck} ok>
              Todos os canais estão normais
            </Empty>
          ) : (
            <div className="list">
              {data.failing.map((f) => (
                <Link key={f.id} to={`/channels/${f.id}`} className="list-item" style={{ color: "inherit" }}>
                  <div className="feed-body">
                    <div className="feed-title truncate">{f.name}</div>
                    <div className="feed-sub">{reason(f.reason) || f.id}</div>
                  </div>
                  <StateChip state={f.state} />
                  <span className="time">{ago(f.since)}</span>
                </Link>
              ))}
            </div>
          )}
        </section>

        <section className="card">
          <div className="card-head">
            <h2>
              <Activity size={16} /> Atividade recente
            </h2>
            <div className="spacer" />
            <Link to="/events" className="small">
              Ver tudo
            </Link>
          </div>
          {data.recent_events.length === 0 ? (
            <Empty>Sem eventos</Empty>
          ) : (
            <div className="list">
              {data.recent_events.slice(0, 8).map((e) => {
                const look = eventLook(e);
                return (
                  <div key={e.id} className="list-item">
                    <span className={`feed-icon ${look.cls}`}>
                      <look.icon size={14} />
                    </span>
                    <div className="feed-body">
                      <div className="feed-title truncate">
                        {e.channel_id ? (
                          <Link to={`/channels/${e.channel_id}`}>{e.channel_id}</Link>
                        ) : (
                          e.worker_id
                        )}
                        <span className="muted"> · {EVENT_LABEL[e.type] ?? e.type}</span>
                      </div>
                      <div className="feed-sub truncate">{describeEvent(e)}</div>
                    </div>
                    <span className="time">{ago(e.at)}</span>
                  </div>
                );
              })}
            </div>
          )}
        </section>
      </div>

      <section>
        <div className="card-head">
          <h2>
            <Server size={16} /> Servidores
          </h2>
        </div>
        {data.workers.items.length === 0 ? (
          <div className="card">
            <Empty icon={Server}>Nenhum servidor conectado</Empty>
          </div>
        ) : (
          <div className="worker-grid">
            {data.workers.items.map((w) => (
              <div key={w.id} className="card">
                <div className="row">
                  <strong>{w.id}</strong>
                  <div className="spacer" />
                  <WorkerChip state={w.state} />
                </div>
                <div className="worker-meters">
                  <Meter label="CPU" percent={w.resources.cpu_percent} />
                  <Meter
                    label="RAM"
                    percent={
                      w.resources.memory_total_bytes
                        ? (w.resources.memory_used_bytes / w.resources.memory_total_bytes) * 100
                        : null
                    }
                  />
                  {w.resources.gpu_percent !== null && (
                    <Meter label="GPU" percent={w.resources.gpu_percent} />
                  )}
                </div>
                <div className="worker-stats">
                  <span>
                    <strong>{w.channels}</strong> canais
                  </span>
                  <span>↓ {bitrate(w.resources.network_in_bps)}</span>
                  <span>↑ {bitrate(w.resources.network_out_bps)}</span>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
    </>
  );
}
