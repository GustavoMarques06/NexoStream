import {
  Activity,
  Archive,
  CircleHelp,
  CalendarDays,
  Cpu,
  LayoutDashboard,
  ShieldCheck,
  Tv,
  Users,
  ArrowLeft,
  Check,
  Copy,
  Lock,
  Pencil,
  Play,
  Radio,
  RotateCcw,
  Square,
  Trash2,
  Waves,
} from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api } from "../api";
import { ChannelForm } from "../components/ChannelForm";
import { Player } from "../components/Player";
import { Recordings } from "../components/Recordings";
import { Empty, ErrorBox, eventLook, Modal, Sparkline, StateChip } from "../components/ui";
import { ago, bitrateParts, describeEvent, EVENT_LABEL, reason } from "../format";
import { useApi } from "../hooks";
import { seed, useLive, useLiveEvents } from "../live";
import { useCan } from "../me";
import { usePlaybackToken, withToken } from "../playback";
import {
  AuthTab,
  DVRTab,
  EPGTab,
  InputTab,
  OutputTab,
  SessionsTab,
  TranscoderTab,
  type TabProps,
} from "./channel/tabs";

const CHANNEL_TABS = [
  { id: "overview", label: "Overview", icon: LayoutDashboard },
  { id: "input", label: "Input", icon: Radio },
  { id: "transcoder", label: "Transcoder", icon: Cpu },
  { id: "dvr", label: "DVR", icon: Archive },
  { id: "output", label: "Output", icon: Tv },
  { id: "epg", label: "EPG", icon: CalendarDays },
  { id: "auth", label: "Auth", icon: ShieldCheck },
  { id: "sessions", label: "Play sessions", icon: Users },
] as const;
type ChannelTab = (typeof CHANNEL_TABS)[number]["id"];
const TAB_DOCS: Record<ChannelTab, string> = {
  overview: "estados",
  input: "fontes-failover",
  transcoder: "transcodificacao",
  dvr: "dvr",
  output: "saidas",
  epg: "epg",
  auth: "autorizacao",
  sessions: "sessoes",
};
import type { Channel, NsEvent, Page } from "../types";

const PROCESSING: Record<string, string> = {
  passthrough: "Passthrough",
  transcode: "Transcodificação",
  audio_transcode: "Áudio → AAC",
};
const OUTPUT_LABEL: Record<string, string> = {
  hls: "HLS",
  mpegts_http: "MPEG-TS HTTP",
  mpegts_udp: "MPEG-TS UDP",
  dvr: "DVR",
};

function CopyButton({ label, value }: { label: string; value: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      className="secondary sm"
      title={value}
      onClick={() => {
        void navigator.clipboard?.writeText(value);
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
    >
      {done ? <Check size={13} /> : <Copy size={13} />} {label}
    </button>
  );
}

export function ChannelDetail() {
  const { id = "" } = useParams();
  const ch = useApi<Channel>(`/channels/${id}`, 15000);
  const events = useApi<Page<NsEvent>>(`/channels/${id}/events?limit=30`);
  const live = useLive();
  const [editing, setEditing] = useState(false);
  // Timeshift / catch-up: playback start (unix seconds); null = live.
  const [dvrFrom, setDvrFrom] = useState<number | null>(null);
  const [dvrTo, setDvrTo] = useState<number | null>(null);
  const [params, setParams] = useSearchParams();
  const tab: ChannelTab = CHANNEL_TABS.some((t) => t.id === params.get("tab"))
    ? (params.get("tab") as ChannelTab)
    : "overview";
  const setTab = (t: ChannelTab) => setParams(t === "overview" ? {} : { tab: t }, { replace: true });
  const [actionError, setActionError] = useState<unknown>(null);
  const canWrite = useCan("channels:write");
  const canOperate = useCan("channels:operate");
  const navigate = useNavigate();
  const playback = usePlaybackToken(ch.data);

  useEffect(() => {
    if (ch.data) seed([ch.data.status]);
  }, [ch.data]);
  useLiveEvents((e) => {
    if (e.channel_id === id) {
      void events.reload();
      void ch.reload();
    }
  });

  if (ch.error) return <ErrorBox error={ch.error} />;
  const c = ch.data;
  if (!c) return null;
  const st = live.status(c.id) ?? c.status;
  const [bv, bu] = bitrateParts(st.input?.bitrate_bps);

  const act = async (path: string, body?: unknown) => {
    setActionError(null);
    try {
      await api.post(`/channels/${c.id}/${path}`, body);
      void ch.reload();
    } catch (e) {
      setActionError(e);
    }
  };
  const remove = async () => {
    if (!confirm(`Remover o canal "${c.name}"?`)) return;
    try {
      await api.del(`/channels/${c.id}`);
      navigate("/channels");
    } catch (e) {
      setActionError(e);
    }
  };

  const tabProps: TabProps = {
    c,
    st,
    canWrite,
    canOperate,
    token: playback.token,
    history: live.history(c.id),
    onSaved: (saved) => ch.setData(saved),
    onWatch: (from, to) => {
      setDvrFrom(from);
      setDvrTo(to ?? null);
      setTab("overview");
    },
  };
  const video = st.input?.video;
  const audio = st.input?.audio?.[0];

  return (
    <>
      <Link to="/channels" className="back">
        <ArrowLeft size={13} /> Canais
      </Link>
      <header className="page-head">
        <div>
          <h1 className="row" style={{ gap: 12 }}>
            {c.name} <StateChip state={st.state} />
            {c.drm?.enabled && (
              <span
                className="tag accent row"
                style={{ gap: 4 }}
                title="HLS e DVR criptografados (AES-128) com token"
              >
                <Lock size={12} /> DRM
              </span>
            )}
          </h1>
          <div className="sub">
            {c.group || c.id}
            {st.since && st.state !== "STOPPED" && ` · ${reason(st.reason)} ${ago(st.since)}`}
          </div>
        </div>
        <div className="spacer" />
        {canOperate && c.enabled && (
          <>
            <button className="secondary" onClick={() => void act("restart")}>
              <RotateCcw size={15} /> Reiniciar
            </button>
            <button className="secondary" onClick={() => void act("stop")}>
              <Square size={15} /> Parar
            </button>
          </>
        )}
        {canOperate && !c.enabled && (
          <button onClick={() => void act("start")}>
            <Play size={15} /> Iniciar
          </button>
        )}
        {canWrite && (
          <button className="secondary" onClick={() => setEditing(true)}>
            <Pencil size={15} /> Editar
          </button>
        )}
        {canWrite && !c.enabled && (
          <button className="danger" onClick={() => void remove()} title="Remover canal">
            <Trash2 size={15} />
          </button>
        )}
      </header>

      <ErrorBox error={actionError} />
      {st.last_error && st.state !== "RUNNING" && (
        <div className="alert warn">
          <Activity size={16} />
          <span className="truncate" title={st.last_error}>
            {st.last_error}
          </span>
        </div>
      )}

      <div className="tabs ch-tabs">
        <Link className="tab-help" to={`/docs/${TAB_DOCS[tab]}`} title="Ajuda sobre esta aba">
          <CircleHelp size={15} /> Ajuda
        </Link>
        {CHANNEL_TABS.map((t) => (
          <button key={t.id} className={tab === t.id ? "on" : ""} onClick={() => setTab(t.id)}>
            <t.icon size={15} />
            {t.label}
            {t.id === "sessions" && st.viewers > 0 && <span className="tag accent">{st.viewers}</span>}
          </button>
        ))}
      </div>

      {tab === "overview" && (
        <>
          <div className="grid grid-detail">
            <div className="stack">
              <ErrorBox error={playback.error} />
              <Player
                key={`${dvrFrom ?? "live"}-${dvrTo ?? ""}-${playback.token ?? ""}`}
                src={
                  !playback.ready
                    ? undefined
                    : dvrFrom !== null && st.outputs?.dvr
                      ? withToken(
                          `${st.outputs.dvr}index.m3u8?from=${dvrFrom}${dvrTo ? `&to=${dvrTo}` : ""}`,
                          playback.token,
                        )
                      : st.state !== "STOPPED"
                        ? withToken(st.outputs?.hls, playback.token)
                        : undefined
                }
                mode={dvrFrom !== null ? "dvr" : "live"}
                onGoLive={() => {
                  setDvrFrom(null);
                  setDvrTo(null);
                }}
              />
              {c.dvr.enabled && st.outputs?.dvr && (
                <Recordings
                  base={st.outputs.dvr}
                  retentionHours={c.dvr.retention_hours}
                  playingFrom={dvrFrom}
                  onPlay={(f) => {
                    setDvrFrom(f);
                    setDvrTo(null);
                  }}
                />
              )}
              {st.outputs && (
                <div className="outputs" style={{ marginTop: 0 }}>
                  {Object.entries(st.outputs).map(([k, v]) => (
                    <CopyButton
                      key={k}
                      label={
                        (OUTPUT_LABEL[k] ?? k) + (playback.token && k !== "mpegts_udp" ? " com token" : "")
                      }
                      value={
                        !playback.token || k === "mpegts_udp"
                          ? v
                          : (withToken(k === "dvr" ? `${v}index.m3u8` : v, playback.token) ?? v)
                      }
                    />
                  ))}
                  {playback.expiresAt && (
                    <span
                      className="small muted row"
                      style={{ gap: 4 }}
                      title="Token de reprodução gerado pelo painel"
                    >
                      <Lock size={12} /> token válido até{" "}
                      {new Date(playback.expiresAt).toLocaleTimeString("pt-BR", {
                        hour: "2-digit",
                        minute: "2-digit",
                      })}
                    </span>
                  )}
                </div>
              )}
            </div>

            <div className="stack">
              <section className="card">
                <div className="card-head">
                  <h2>
                    <Waves size={16} /> Sinal
                  </h2>
                  <div className="spacer" />
                  {st.processing && (
                    <span className="tag accent">{PROCESSING[st.processing] ?? st.processing}</span>
                  )}
                </div>
                <div className="row" style={{ alignItems: "flex-end" }}>
                  <div className="signal-big">
                    {bv} <small>{bu}</small>
                  </div>
                  <div className="spacer" />
                  <Sparkline values={live.history(c.id)} width={150} height={44} />
                </div>
                <div className="facts">
                  <div className="fact">
                    <div className="fact-label">Vídeo</div>
                    <div className="fact-value">
                      {video
                        ? `${video.codec.toUpperCase()}${video.height ? ` · ${video.height}p` : ""}${video.fps ? ` · ${video.fps}` : ""}`
                        : "—"}
                    </div>
                  </div>
                  <div className="fact">
                    <div className="fact-label">Áudio</div>
                    <div className="fact-value">
                      {audio
                        ? `${audio.codec.toUpperCase()}${audio.channels ? ` · ${audio.channels}ch` : ""}`
                        : "—"}
                    </div>
                  </div>
                  <div className="fact">
                    <div className="fact-label">Erros CC / min</div>
                    <div className={`fact-value ${st.input?.cc_errors_last_minute ? "bad" : ""}`}>
                      {st.input?.cc_errors_last_minute ?? "—"}
                    </div>
                  </div>
                  <div className="fact">
                    <div className="fact-label">Último pacote</div>
                    <div className="fact-value">{ago(st.input?.last_packet_at)}</div>
                  </div>
                </div>
              </section>

              <section className="card">
                <div className="card-head">
                  <h2>
                    <Radio size={16} /> Fontes
                  </h2>
                  <div className="spacer" />
                  <span
                    className="small muted"
                    title={`Troca após ${c.failover.no_packets_seconds + c.failover.confirm_window_seconds} s sem sinal. Volta à principal após ${c.failover.primary_stable_seconds} s estável.`}
                  >
                    failover automático
                  </span>
                </div>
                {[...c.sources]
                  .sort((a, b) => a.priority - b.priority)
                  .map((s) => {
                    const active = st.active_source?.priority === s.priority && st.state !== "STOPPED";
                    return (
                      <div key={s.id ?? s.priority} className={`source ${active ? "active" : ""}`}>
                        <span className="prio">{s.priority}</span>
                        <div style={{ minWidth: 0, flex: 1 }}>
                          <div className="mono truncate">{s.url}</div>
                          <div className="small muted">
                            {s.priority === 1 ? "Principal" : "Backup"}
                            {s.interface ? ` · ${s.interface}` : ""}
                          </div>
                        </div>
                        {active ? (
                          <span className="chip s-running">
                            <span className="dot" /> No ar
                          </span>
                        ) : (
                          canOperate &&
                          c.enabled && (
                            <button
                              className="secondary sm"
                              onClick={() =>
                                void act("switch-source", { priority: s.priority, reason: "painel" })
                              }
                            >
                              Ativar
                            </button>
                          )
                        )}
                      </div>
                    );
                  })}
              </section>
            </div>
          </div>

          <section className="card">
            <div className="card-head">
              <h2>
                <Activity size={16} /> Linha do tempo
              </h2>
            </div>
            {events.data?.items.length ? (
              <div className="timeline">
                {events.data.items.map((e) => {
                  const look = eventLook(e);
                  return (
                    <div key={e.id} className={`tl-item ${look.cls}`}>
                      <span className="tl-dot" />
                      <div className="feed-body">
                        <div className="feed-title">{EVENT_LABEL[e.type] ?? e.type}</div>
                        <div className="feed-sub">{describeEvent(e)}</div>
                      </div>
                      <span className="time" title={new Date(e.at).toLocaleString("pt-BR")}>
                        {new Date(e.at).toLocaleTimeString("pt-BR")}
                      </span>
                    </div>
                  );
                })}
              </div>
            ) : (
              <Empty>Sem eventos</Empty>
            )}
          </section>
        </>
      )}
      {tab === "input" && <InputTab {...tabProps} />}
      {tab === "transcoder" && <TranscoderTab {...tabProps} />}
      {tab === "dvr" && <DVRTab {...tabProps} />}
      {tab === "output" && <OutputTab {...tabProps} />}
      {tab === "epg" && <EPGTab {...tabProps} />}
      {tab === "auth" && <AuthTab {...tabProps} />}
      {tab === "sessions" && <SessionsTab {...tabProps} />}

      {editing && (
        <Modal title={`Editar ${c.name}`} onClose={() => setEditing(false)} wide>
          <ChannelForm
            channel={c}
            onCancel={() => setEditing(false)}
            onSaved={(saved) => {
              ch.setData(saved);
              setEditing(false);
            }}
          />
        </Modal>
      )}
    </>
  );
}
