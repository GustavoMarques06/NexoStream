import { Thumb } from "../components/Thumb";
import { LayoutGrid, List, Play, Plus, RotateCcw, Search, Square, Tv } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api, qs } from "../api";
import { ChannelForm } from "../components/ChannelForm";
import { Empty, ErrorBox, Modal, PageHeader, Sparkline, StateChip, stateClass } from "../components/ui";
import { bitrate, bitrateParts, reason } from "../format";
import { useApi } from "../hooks";
import { seed, useLive, useLiveEvents } from "../live";
import { useCan } from "../me";
import type { Channel, ChannelState, Page } from "../types";

type Group = "all" | "up" | "attention" | "stopped";
const GROUPS: { id: Group; label: string; states: ChannelState[] }[] = [
  { id: "all", label: "Todos", states: [] },
  { id: "up", label: "No ar", states: ["RUNNING", "RECOVERING"] },
  { id: "attention", label: "Atenção", states: ["FAILED", "DEGRADED", "SWITCHING", "ON_BACKUP", "STARTING"] },
  { id: "stopped", label: "Parados", states: ["STOPPED"] },
];

const PROCESSING: Record<string, string> = {
  passthrough: "Passthrough",
  transcode: "Transcodificação",
  audio_transcode: "Áudio → AAC",
};

export function Channels() {
  const [params, setParams] = useSearchParams();
  const [q, setQ] = useState(params.get("q") ?? "");
  const legacyState = params.get("state");
  const initialGroup =
    (params.get("view") as Group | null) ??
    (legacyState ? (GROUPS.find((g) => g.states.includes(legacyState as ChannelState))?.id ?? "all") : "all");
  const [group, setGroup] = useState<Group>(initialGroup);
  const [layout, setLayout] = useState<"grid" | "list">("grid");
  const { data, error, reload } = useApi<Page<Channel>>(
    "/channels" + qs({ q: params.get("q"), limit: 200 }),
    15000,
  );
  const live = useLive();
  const [creating, setCreating] = useState(false);
  const [actionError, setActionError] = useState<unknown>(null);
  const canWrite = useCan("channels:write");
  const canOperate = useCan("channels:operate");
  const navigate = useNavigate();

  useEffect(() => {
    if (data) seed(data.items.map((c) => c.status));
  }, [data]);
  useLiveEvents((e) => e.type === "channel.state_changed" && void reload());

  const items: Channel[] = (data?.items ?? []).map((c) => ({ ...c, status: live.status(c.id) ?? c.status }));
  const counts = Object.fromEntries(
    GROUPS.map((g) => [
      g.id,
      g.states.length ? items.filter((c) => g.states.includes(c.status.state)).length : items.length,
    ]),
  ) as Record<Group, number>;
  const shown = items.filter((c) => {
    const g = GROUPS.find((x) => x.id === group)!;
    return !g.states.length || g.states.includes(c.status.state);
  });

  const search = (v: string) => {
    const p = new URLSearchParams(params);
    if (v) p.set("q", v);
    else p.delete("q");
    p.delete("state");
    setParams(p);
  };

  const act = async (e: React.MouseEvent, c: Channel, action: "start" | "stop" | "restart") => {
    e.preventDefault();
    e.stopPropagation();
    setActionError(null);
    try {
      await api.post(`/channels/${c.id}/${action}`);
      void reload();
    } catch (err) {
      setActionError(err);
    }
  };

  const actions = (c: Channel) =>
    canOperate && (
      <>
        {c.enabled ? (
          <>
            <button className="icon" title="Reiniciar" onClick={(e) => void act(e, c, "restart")}>
              <RotateCcw size={15} />
            </button>
            <button className="icon" title="Parar" onClick={(e) => void act(e, c, "stop")}>
              <Square size={15} />
            </button>
          </>
        ) : (
          <button className="icon" title="Iniciar" onClick={(e) => void act(e, c, "start")}>
            <Play size={15} />
          </button>
        )}
      </>
    );

  return (
    <>
      <PageHeader title="Canais" sub={data ? `${items.length} cadastrados` : undefined}>
        {canWrite && (
          <button onClick={() => setCreating(true)}>
            <Plus size={16} /> Novo canal
          </button>
        )}
      </PageHeader>

      <div className="toolbar">
        <div className="segmented">
          {GROUPS.map((g) => (
            <button key={g.id} className={group === g.id ? "on" : ""} onClick={() => setGroup(g.id)}>
              {g.label} <span className="count">{counts[g.id]}</span>
            </button>
          ))}
        </div>
        <div className="search">
          <Search size={15} />
          <input
            placeholder="Buscar canal"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && search(q)}
            onBlur={() => search(q)}
          />
        </div>
        <div className="spacer" />
        <div className="segmented">
          <button className={layout === "grid" ? "on" : ""} onClick={() => setLayout("grid")} title="Cards">
            <LayoutGrid size={15} />
          </button>
          <button className={layout === "list" ? "on" : ""} onClick={() => setLayout("list")} title="Lista">
            <List size={15} />
          </button>
        </div>
      </div>

      <ErrorBox error={error ?? actionError} />

      {data && shown.length === 0 && (
        <div className="card">
          <Empty icon={Tv}>
            {items.length === 0 ? "Nenhum canal cadastrado" : "Nenhum canal neste filtro"}
          </Empty>
        </div>
      )}

      {layout === "grid" && shown.length > 0 && (
        <div className="channel-grid">
          {shown.map((c) => {
            const st = c.status;
            return (
              <Link key={c.id} to={`/channels/${c.id}`} className={`ch-card ${stateClass(st.state)}`}>
                <div className="ch-actions">{actions(c)}</div>
                <Thumb c={c} />
                <div className="ch-top">
                  <div style={{ minWidth: 0, flex: 1 }}>
                    <div className="ch-name truncate">{c.name}</div>
                    <div className="ch-meta">
                      <span className="truncate">{c.group || c.id}</span>
                    </div>
                  </div>
                </div>
                <div className="ch-metrics">
                  <div>
                    <div className="ch-bitrate">
                      {bitrateParts(st.input?.bitrate_bps)[0]}
                      <small> {bitrateParts(st.input?.bitrate_bps)[1]}</small>
                    </div>
                    <div className="small muted">{reason(st.reason) || "entrada"}</div>
                  </div>
                  <Sparkline values={live.history(c.id)} width={110} height={36} color="var(--c)" />
                </div>
                <div className="ch-foot">
                  <StateChip state={st.state} />
                  {st.active_source && (
                    <span className={`tag ${st.active_source.priority === 1 ? "" : "accent"}`}>
                      Fonte {st.active_source.priority}
                    </span>
                  )}
                  {st.processing && <span className="tag">{PROCESSING[st.processing] ?? st.processing}</span>}
                  {st.input?.video?.height ? <span className="tag">{st.input.video.height}p</span> : null}
                </div>
              </Link>
            );
          })}
        </div>
      )}

      {layout === "list" && shown.length > 0 && (
        <div className="card flush">
          <table>
            <thead>
              <tr>
                <th>Canal</th>
                <th>Estado</th>
                <th>Fonte</th>
                <th>Processamento</th>
                <th>Bitrate</th>
                <th>Servidor</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {shown.map((c) => (
                <tr key={c.id} style={{ cursor: "pointer" }} onClick={() => navigate(`/channels/${c.id}`)}>
                  <td>
                    <strong>{c.name}</strong>
                    <div className="small muted">{c.group || c.id}</div>
                  </td>
                  <td>
                    <StateChip state={c.status.state} />
                  </td>
                  <td>{c.status.active_source ? `#${c.status.active_source.priority}` : "—"}</td>
                  <td>
                    {c.status.processing ? (PROCESSING[c.status.processing] ?? c.status.processing) : "—"}
                  </td>
                  <td>{bitrate(c.status.input?.bitrate_bps)}</td>
                  <td className="muted">{c.status.worker_id ?? "—"}</td>
                  <td className="actions">{actions(c)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <Modal title="Novo canal" onClose={() => setCreating(false)} wide>
          <ChannelForm
            onCancel={() => setCreating(false)}
            onSaved={(c) => {
              setCreating(false);
              navigate(`/channels/${c.id}`);
            }}
          />
        </Modal>
      )}
    </>
  );
}
