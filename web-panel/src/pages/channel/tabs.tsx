import {
  Activity,
  Archive,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  CalendarDays,
  Check,
  Copy,
  Film,
  Cpu,
  Gauge,
  Globe,
  HardDrive,
  KeyRound,
  ImagePlus,
  Layers,
  Download,
  Link2,
  ListChecks,
  Lock,
  Network,
  Pencil,
  Plus,
  Radio,
  RefreshCw,
  Server,
  ShieldCheck,
  ShieldOff,
  Send,
  Shuffle,
  Timer,
  Trash2,
  Tv,
  UserCheck,
  Users,
  Waves,
  XCircle,
} from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { api } from "../../api";
import { fieldErrors } from "../../components/form";
import { Feature, KV, Panel, Row, SaveBar, Switch } from "../../components/panel";
import { Recordings } from "../../components/Recordings";
import { Empty, ErrorBox, Sparkline } from "../../components/ui";
import { ago, bitrate, bytes, dateTime } from "../../format";
import { useApi } from "../../hooks";
import { withToken } from "../../playback";
import { SessionHistory } from "../Sessions";
import type {
  AuthConfig,
  Channel,
  ChannelStatus,
  DRMConfig,
  DVRConfig,
  DVRStorage,
  EPGConfig,
  EPGStatus,
  Failover,
  Mode,
  OverlayConfig,
  Outputs,
  Page,
  PlaySession,
  Profile,
  Program,
  Source,
} from "../../types";

export interface TabProps {
  c: Channel;
  st: ChannelStatus;
  canWrite: boolean;
  canOperate: boolean;
  token: string | null;
  /** Input bitrate history (bps), newest last. */
  history: number[];
  onSaved: (c: Channel) => void;
  /** Plays a recorded interval (unix seconds) in the Overview player. */
  onWatch: (from: number, to?: number) => void;
}

const PROCESSING: Record<string, string> = {
  passthrough: "Passthrough",
  transcode: "Transcodificação",
  audio_transcode: "Áudio convertido",
};

/** noteText translates the pipeline decision notes (English in the code). */
function noteText(n: string): string {
  const rules: [RegExp, (m: RegExpMatchArray) => string][] = [
    [/^passthrough forced by the operator$/, () => "Passthrough forçado pelo operador"],
    [/^transcode forced by the operator$/, () => "Transcodificação forçada pelo operador"],
    [/^video (\S+) is transcoded to h264$/, (m) => `Vídeo ${m[1]} é transcodificado para H.264`],
    [/^audio (.+) is converted to aac$/, (m) => `Áudio ${m[1]} é convertido para AAC`],
    [/^(\d+) audio tracks kept$/, (m) => `${m[1]} faixas de áudio mantidas`],
  ];
  for (const [re, f] of rules) {
    const m = n.match(re);
    if (m) return f(m);
  }
  return n;
}

/** useEditor keeps a draft of part of the channel and saves it with PATCH. */
function useEditor<T>(c: Channel, initial: T, onSaved: (c: Channel) => void) {
  const [draft, setDraft] = useState<T>(initial);
  const [base, setBase] = useState(JSON.stringify(initial));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [ok, setOk] = useState(false);
  const dirty = JSON.stringify(draft) !== base;
  const save = async (body: unknown) => {
    setSaving(true);
    setError(null);
    try {
      const saved = await api.patch<Channel>(`/channels/${c.id}`, body);
      setBase(JSON.stringify(draft));
      setOk(true);
      setTimeout(() => setOk(false), 2500);
      onSaved(saved);
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };
  const reset = () => {
    setDraft(JSON.parse(base) as T);
    setError(null);
  };
  const errs = fieldErrors(error);
  return {
    draft,
    setDraft,
    dirty,
    saving,
    ok,
    save,
    reset,
    errs,
    error: Object.keys(errs).length ? null : error,
  };
}

function Num({
  value,
  onChange,
  unit,
  min,
  max,
  step = "any",
  disabled,
}: {
  value: number;
  onChange: (n: number) => void;
  unit?: string;
  min?: number;
  max?: number;
  step?: number | "any";
  disabled?: boolean;
}) {
  return (
    <div className="num-unit compact">
      <input
        type="number"
        value={Number.isFinite(value) ? value : ""}
        min={min}
        max={max}
        step={step}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))}
      />
      {unit && <span className="unit">{unit}</span>}
    </div>
  );
}

function CopyLink({
  label,
  value,
  icon: Icon = Link2,
}: {
  label: string;
  value: string;
  icon?: typeof Link2;
}) {
  const [done, setDone] = useState(false);
  return (
    <div className="link-item">
      <div className="link-head">
        <Icon size={14} /> {label}
        <div className="spacer" />
        <button
          className="icon"
          title="Copiar"
          onClick={() => {
            void navigator.clipboard?.writeText(value);
            setDone(true);
            setTimeout(() => setDone(false), 1500);
          }}
        >
          {done ? <Check size={14} /> : <Copy size={14} />}
        </button>
      </div>
      <code className="link-url" title={value}>
        {value}
      </code>
    </div>
  );
}

const unix = (s?: string) => (s ? Math.floor(new Date(s).getTime() / 1000) : 0);
const hhmm = (s: string) => new Date(s).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
const duration = (from: string, to?: string) => {
  const s = Math.max(
    0,
    Math.round(((to ? new Date(to).getTime() : Date.now()) - new Date(from).getTime()) / 1000),
  );
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h ? `${h} h ${m} min` : m ? `${m} min` : `${s} s`;
};
const PUSH_STATE: Record<string, string> = {
  running: "enviando há",
  retrying: "reconectando desde",
  connecting: "conectando desde",
};
const OUTPUT_LABEL: Record<string, string> = {
  hls: "HLS",
  mpegts_http: "MPEG-TS HTTP",
  mpegts_udp: "MPEG-TS UDP",
  dvr: "DVR",
};

// ---------------------------------------------------------------- Input

export function InputTab({ c, st, canWrite, canOperate, onSaved, history }: TabProps) {
  const sorted = [...c.sources].sort((a, b) => a.priority - b.priority);
  const ed = useEditor<{ sources: Source[]; failover: Failover }>(
    c,
    { sources: sorted, failover: c.failover },
    onSaved,
  );
  const [editing, setEditing] = useState(false);
  const [actionError, setActionError] = useState<unknown>(null);
  const status = new Map((st.sources ?? []).map((s) => [s.priority, s]));
  const list = ed.draft.sources;
  const fo = ed.draft.failover;
  const setList = (l: Source[]) =>
    ed.setDraft({ ...ed.draft, sources: l.map((s, k) => ({ ...s, priority: k + 1 })) });
  const setSrc = (i: number, patch: Partial<Source>) =>
    setList(list.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  const move = (i: number, d: number) => {
    const next = [...list];
    const [x] = next.splice(i, 1);
    next.splice(i + d, 0, x!);
    setList(next);
  };
  const setFo = (patch: Partial<Failover>) => ed.setDraft({ ...ed.draft, failover: { ...fo, ...patch } });
  const activate = async (priority: number) => {
    setActionError(null);
    try {
      await api.post(`/channels/${c.id}/switch-source`, { priority, reason: "painel" });
    } catch (e) {
      setActionError(e);
    }
  };
  const input = st.input;
  const v = input?.video;
  const ro = !canWrite;

  return (
    <div className="tab-grid">
      <div>
        <Panel
          icon={Radio}
          title="Fontes"
          sub="Em ordem de prioridade: a 1 é a principal, as demais assumem no failover."
          flush
          actions={
            canWrite &&
            (editing ? (
              <button className="secondary sm" onClick={() => setEditing(false)}>
                <Check size={14} /> Concluir
              </button>
            ) : (
              <button className="secondary sm" onClick={() => setEditing(true)}>
                <Pencil size={14} /> Editar
              </button>
            ))
          }
        >
          <ErrorBox error={actionError} />
          {list.map((s, i) => {
            const ss = status.get(s.priority);
            const err = ed.errs[`sources[${i}].url`];
            const state = ss?.active ? "on" : ss?.listening ? "standby" : ss?.error ? "bad" : "idle";
            return (
              <div key={i} className={`src-row ${ss?.active ? "active" : ""}`}>
                <span className="src-prio">{i + 1}</span>
                <span className={`src-dot ${state}`} title={ss?.error} />
                <div className="src-main">
                  {editing ? (
                    <div className="src-edit">
                      <input
                        className={`mono ${err ? "invalid" : ""}`}
                        value={s.url}
                        placeholder="udp://239.1.1.1:1234"
                        onChange={(e) => setSrc(i, { url: e.target.value })}
                      />
                      <input
                        className="mono"
                        value={s.interface ?? ""}
                        placeholder="interface"
                        onChange={(e) => setSrc(i, { interface: e.target.value || undefined })}
                      />
                    </div>
                  ) : (
                    <div className="src-url mono">{s.url || "—"}</div>
                  )}
                  <div className="src-meta">
                    {i === 0 ? "Principal" : `Backup ${i}`}
                    {s.interface && !editing && ` · ${s.interface}`}
                    {ss?.active && " · no ar"}
                    {!ss?.active && ss?.listening && " · em espera"}
                    {ss?.error && <span className="bad"> · {ss.error}</span>}
                    {ss?.last_packet_at && ` · último pacote ${ago(ss.last_packet_at)}`}
                  </div>
                  {err && <div className="prow-err">{err}</div>}
                </div>
                <div className="src-rate">{ss?.active || ss?.listening ? bitrate(ss.bitrate_bps) : ""}</div>
                <div className="src-actions">
                  {editing ? (
                    <>
                      <button className="icon" disabled={i === 0} onClick={() => move(i, -1)} title="Subir">
                        <ArrowUp size={15} />
                      </button>
                      <button
                        className="icon"
                        disabled={i === list.length - 1}
                        onClick={() => move(i, 1)}
                        title="Descer"
                      >
                        <ArrowDown size={15} />
                      </button>
                      <button
                        className="icon"
                        disabled={list.length === 1}
                        title="Remover"
                        onClick={() => setList(list.filter((_, j) => j !== i))}
                      >
                        <Trash2 size={15} />
                      </button>
                    </>
                  ) : (
                    canOperate &&
                    c.enabled &&
                    !ss?.active &&
                    !ed.dirty && (
                      <button className="secondary sm" onClick={() => void activate(s.priority)}>
                        <Shuffle size={13} /> Ativar
                      </button>
                    )
                  )}
                </div>
              </div>
            );
          })}
          {editing && list.length < 8 && (
            <div className="src-add">
              <button
                className="ghost sm"
                onClick={() => setList([...list, { priority: list.length + 1, url: "" }])}
              >
                <Plus size={14} /> Adicionar fonte de backup
              </button>
            </div>
          )}
        </Panel>

        <Panel icon={Shuffle} title="Failover" sub="Quando trocar para o backup e quando voltar." flush>
          <Row
            title="Sem pacotes"
            hint="Fonte considerada sem sinal depois desse tempo."
            err={ed.errs["failover.no_packets_seconds"]}
          >
            <Num
              value={fo.no_packets_seconds}
              unit="s"
              disabled={ro}
              onChange={(n) => setFo({ no_packets_seconds: n })}
            />
          </Row>
          <Row
            title="Confirmação"
            hint="Espera antes de trocar, para ignorar falhas curtas."
            err={ed.errs["failover.confirm_window_seconds"]}
          >
            <Num
              value={fo.confirm_window_seconds}
              unit="s"
              disabled={ro}
              onChange={(n) => setFo({ confirm_window_seconds: n })}
            />
          </Row>
          <Row
            title="Taxa baixa"
            hint="Falha quando a taxa fica abaixo dessa fração da média por esse tempo."
            err={ed.errs["failover.low_bitrate_ratio"] ?? ed.errs["failover.low_bitrate_seconds"]}
          >
            <div className="pair">
              <Num
                value={fo.low_bitrate_ratio}
                unit="× média"
                disabled={ro}
                onChange={(n) => setFo({ low_bitrate_ratio: n })}
              />
              <Num
                value={fo.low_bitrate_seconds}
                unit="s"
                disabled={ro}
                onChange={(n) => setFo({ low_bitrate_seconds: n })}
              />
            </div>
          </Row>
          <Row
            title="Faixa ausente"
            hint="Vídeo ou áudio sumido por esse tempo."
            err={ed.errs["failover.missing_stream_seconds"]}
          >
            <Num
              value={fo.missing_stream_seconds}
              unit="s"
              disabled={ro}
              onChange={(n) => setFo({ missing_stream_seconds: n })}
            />
          </Row>
          <Row title="Voltar para a principal" hint="Retorna sozinho quando a fonte 1 ficar estável.">
            <Switch
              label="Voltar para a principal"
              checked={fo.return_to_primary}
              disabled={ro}
              onChange={(b) => setFo({ return_to_primary: b })}
            />
          </Row>
          {fo.return_to_primary && (
            <Row
              title="Principal estável por"
              hint="Tempo de sinal bom antes de voltar."
              err={ed.errs["failover.primary_stable_seconds"]}
            >
              <Num
                value={fo.primary_stable_seconds}
                unit="s"
                step={1}
                disabled={ro}
                onChange={(n) => setFo({ primary_stable_seconds: n })}
              />
            </Row>
          )}
        </Panel>
      </div>

      <aside>
        <Panel
          icon={Waves}
          title="Sinal recebido"
          sub={input ? `atualizado ${ago(input.last_packet_at)}` : undefined}
        >
          {input ? (
            <>
              <div className="signal-head">
                <div className="signal-rate">{bitrate(input.bitrate_bps)}</div>
                <Sparkline values={history} width={130} height={36} />
              </div>
              <KV
                items={[
                  ["Vídeo", v ? v.codec.toUpperCase() : "—"],
                  [
                    "Resolução",
                    v?.width ? `${v.width}×${v.height}${v.interlaced ? " (entrelaçado)" : ""}` : "—",
                  ],
                  ["Quadros", v?.fps ? `${v.fps} fps` : "—"],
                  ...input.audio.map((a, i): [ReactNode, ReactNode] => [
                    `Áudio ${i + 1}`,
                    `${a.codec.toUpperCase()}${a.channels ? ` · ${a.channels}ch` : ""}${a.language ? ` · ${a.language}` : ""}`,
                  ]),
                  ...(input.subtitles ?? []).map((x, i): [ReactNode, ReactNode] => [
                    `Legenda ${i + 1}`,
                    `${x.codec === "teletext" ? "Teletexto" : "DVB"}${x.language ? ` · ${x.language}` : ""}`,
                  ]),
                  ["Erros CC / min", input.cc_errors_last_minute, input.cc_errors_last_minute ? "bad" : ""],
                ]}
              />
            </>
          ) : (
            <Empty icon={Waves}>Sem sinal analisado: o canal não está recebendo agora.</Empty>
          )}
        </Panel>
      </aside>
      <div className="tab-foot">
        <ErrorBox error={ed.error} />
        <SaveBar
          {...ed}
          onReset={() => {
            ed.reset();
            setEditing(false);
          }}
          onSave={() =>
            void ed.save({
              sources: list.map((s, i) => ({ ...s, priority: i + 1, interface: s.interface || undefined })),
              failover: fo,
            })
          }
        />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- Transcoder

const MODES: { id: Mode; title: string; hint: string }[] = [
  { id: "auto", title: "Automático", hint: "Repassa H.264/AAC e converte só o que for preciso." },
  { id: "passthrough", title: "Passthrough", hint: "Nunca recodifica. Menor uso de CPU." },
  { id: "transcode", title: "Transcodificar", hint: "Sempre recodifica com o perfil escolhido." },
];

export function TranscoderTab({ c, st, canWrite, onSaved }: TabProps) {
  const profiles = useApi<Page<Profile>>("/transcode-profiles");
  const ed = useEditor<{ mode: Mode; profiles: string[]; overlay: OverlayConfig }>(
    c,
    { mode: c.mode, profiles: c.profiles, overlay: c.overlay ?? { enabled: false } },
    onSaved,
  );
  const ov = ed.draft.overlay;
  const setOv = (o: Partial<OverlayConfig>) => ed.setDraft({ ...ed.draft, overlay: { ...ov, ...o } });
  const p = st.pipeline;
  const v = st.input?.video;
  const all = [...(profiles.data?.items ?? [])].sort((a, b) => b.video.height - a.video.height);
  const sel = ed.draft.profiles;
  const ladder = ed.draft.mode === "transcode";
  const toggle = (id: string) =>
    ed.setDraft({ ...ed.draft, profiles: sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id] });
  const setMode = (m: Mode) =>
    ed.setDraft({ ...ed.draft, mode: m, profiles: m === "transcode" ? sel : sel.slice(0, 1) });
  const outs = [
    c.outputs.hls && "HLS",
    c.outputs.mpegts_http && "MPEG-TS HTTP",
    c.outputs.mpegts_udp && "MPEG-TS UDP",
    c.dvr.enabled && "DVR",
  ].filter(Boolean) as string[];
  const rends = p?.renditions ?? [];
  return (
    <div className="tab-grid">
      <div>
        <Panel
          icon={Activity}
          title="Processamento atual"
          sub={p ? `${p.engine} · rodando há ${duration(p.started_at)}` : "O pipeline não está rodando."}
        >
          <div className="flow">
            <div className="flow-node">
              <div className="flow-label">Entrada</div>
              <div className="flow-main">{v ? v.codec.toUpperCase() : "—"}</div>
              <div className="flow-sub">
                {v?.width ? `${v.width}×${v.height}` : ""}
                {v?.fps ? ` · ${v.fps} fps` : ""}
              </div>
              <div className="flow-sub">
                {st.input?.audio.length
                  ? `${st.input.audio.length} áudio: ${st.input.audio.map((a) => a.codec.toUpperCase()).join(", ")}`
                  : "sem áudio"}
              </div>
            </div>
            <ArrowRight className="flow-arrow" size={18} />
            <div className={`flow-node ${p ? "accent" : ""}`}>
              <div className="flow-label">Processamento</div>
              <div className="flow-main">
                {!p
                  ? "parado"
                  : rends.length > 1
                    ? `ABR · ${rends.length} qualidades`
                    : (PROCESSING[p.processing] ?? p.processing)}
              </div>
              {p && rends.length > 1 ? (
                rends.map((r) => (
                  <div key={r.profile} className="flow-sub">
                    {r.height}p · {bitrate(r.bitrate_bps)} · {r.encoder}
                  </div>
                ))
              ) : p ? (
                <>
                  <div className="flow-sub">
                    Vídeo:{" "}
                    {p.video_copy
                      ? "repassado"
                      : `${p.video?.width ?? "?"}×${p.video?.height ?? "?"} · ${bitrate(p.video?.bitrate_bps)}${p.encoder ? ` · ${p.encoder}` : ""}`}
                  </div>
                  <div className="flow-sub">
                    Áudio: {p.audio_copy ? "repassado" : `AAC ${bitrate(p.audio?.bitrate_bps)}`} ·{" "}
                    {p.audio_tracks} faixa
                    {p.audio_tracks === 1 ? "" : "s"}
                  </div>
                </>
              ) : null}
            </div>
            <ArrowRight className="flow-arrow" size={18} />
            <div className="flow-node">
              <div className="flow-label">Saídas</div>
              <div className="flow-main">{outs.length ? outs[0] : "—"}</div>
              <div className="flow-sub">{outs.slice(1).join(" · ")}</div>
              {rends.length > 1 && (
                <div className="flow-sub">playlist mestre + {rends.length} qualidades</div>
              )}
              {c.drm?.enabled && (
                <div className="flow-sub">
                  <Lock size={11} /> AES-128
                </div>
              )}
            </div>
          </div>
          {!!p?.notes?.length && (
            <div className="notes">
              {p.notes.map((n) => (
                <span key={n} className="tag">
                  {noteText(n)}
                </span>
              ))}
            </div>
          )}
        </Panel>

        <Panel
          icon={Cpu}
          title="Modo de processamento"
          sub="Mudar o modo ou as qualidades reinicia o pipeline por alguns segundos."
        >
          <div className="mode-picker">
            {MODES.map((m) => (
              <button
                key={m.id}
                type="button"
                className={`mode-option ${ed.draft.mode === m.id ? "on" : ""}`}
                disabled={!canWrite}
                onClick={() => setMode(m.id)}
              >
                {m.title}
                <span>{m.hint}</span>
              </button>
            ))}
          </div>
        </Panel>

        {ladder && (
          <Panel
            icon={Layers}
            title="Qualidades (ABR)"
            sub="Marque uma ou mais. Com duas ou mais, o player escolhe a qualidade pela banda disponível."
            flush
          >
            {all.map((x) => {
              const on = sel.includes(x.id);
              return (
                <Row
                  key={x.id}
                  title={
                    <span className="row" style={{ gap: 8 }}>
                      {x.id}
                      <span className="tag">{x.video.height}p</span>
                    </span>
                  }
                  hint={`${x.video.width}×${x.video.height} · ${bitrate(x.video.bitrate_bps)} · ${x.video.codec} ${x.video.profile} · áudio ${bitrate(x.audio.bitrate_bps)} · encoder ${x.encoder}`}
                >
                  <Switch
                    label={x.id}
                    checked={on}
                    disabled={!canWrite || (on && sel.length === 1)}
                    onChange={() => toggle(x.id)}
                  />
                </Row>
              );
            })}
            {ed.errs.profiles && <p className="panel-note bad">{ed.errs.profiles}</p>}
          </Panel>
        )}
        <Panel
          icon={ImagePlus}
          title="Logo"
          sub="Marca d'água gravada no vídeo, em todas as qualidades."
          actions={
            canWrite && (
              <Switch
                label="Logo"
                checked={ov.enabled}
                disabled={!ladder && !ov.enabled}
                onChange={(on) => setOv({ enabled: on, position: ov.position ?? "top-right" })}
              />
            )
          }
        >
          {!ladder && (
            <p className="panel-note">
              O logo exige o modo Transcodificar: nos outros modos o vídeo pode ser copiado sem decodificar.
            </p>
          )}
          {ed.errs["overlay.enabled"] && <p className="panel-note bad">{ed.errs["overlay.enabled"]}</p>}
          {ov.enabled && (
            <>
              <Row
                title="Arquivo"
                hint="Nome de um PNG ou JPG na pasta de logos dos servidores de mídia (NEXOSTREAM_OVERLAY_DIR). Sem pastas."
                err={ed.errs["overlay.image"]}
              >
                <input
                  className="mono"
                  value={ov.image ?? ""}
                  placeholder="logo.png"
                  disabled={!canWrite}
                  onChange={(e) => setOv({ image: e.target.value })}
                />
              </Row>
              <Row title="Posição" err={ed.errs["overlay.position"]}>
                <select
                  value={ov.position ?? "top-right"}
                  disabled={!canWrite}
                  onChange={(e) => setOv({ position: e.target.value as OverlayConfig["position"] })}
                >
                  <option value="top-left">Superior esquerda</option>
                  <option value="top-right">Superior direita</option>
                  <option value="bottom-left">Inferior esquerda</option>
                  <option value="bottom-right">Inferior direita</option>
                </select>
              </Row>
              <Row
                title="Margem"
                hint="Distância das bordas, em pixels da imagem de origem."
                err={ed.errs["overlay.margin"]}
              >
                <Num
                  value={ov.margin ?? 0}
                  unit="px"
                  min={0}
                  max={1000}
                  step={1}
                  disabled={!canWrite}
                  onChange={(n) => setOv({ margin: n })}
                />
              </Row>
              <Row title="Largura" hint="0 mantém o tamanho do arquivo." err={ed.errs["overlay.width"]}>
                <Num
                  value={ov.width ?? 0}
                  unit="px"
                  min={0}
                  max={3840}
                  step={1}
                  disabled={!canWrite}
                  onChange={(n) => setOv({ width: n })}
                />
              </Row>
              <Row title="Opacidade" hint="De 0,05 a 1 (0 = opaco)." err={ed.errs["overlay.opacity"]}>
                <Num
                  value={ov.opacity ?? 0}
                  min={0}
                  max={1}
                  step={0.05}
                  disabled={!canWrite}
                  onChange={(n) => setOv({ opacity: n })}
                />
              </Row>
            </>
          )}
        </Panel>
      </div>

      <aside>
        {ladder ? (
          <Panel icon={Layers} title="Resumo">
            <KV
              items={[
                ["Qualidades", sel.length],
                [
                  "Maior",
                  all.find((x) => sel.includes(x.id))
                    ? `${all.find((x) => sel.includes(x.id))!.video.height}p`
                    : "—",
                ],
                [
                  "Banda total (todas)",
                  bitrate(
                    all
                      .filter((x) => sel.includes(x.id))
                      .reduce((n, x) => n + x.video.bitrate_bps + x.audio.bitrate_bps, 0),
                  ),
                ],
              ]}
            />
            <p className="aside-note">
              Cada qualidade é uma codificação a mais: o consumo de CPU/GPU cresce com o número de qualidades
              e a resolução. O MPEG-TS usa a qualidade mais alta. O DVR também, a não ser que você ligue
              "Gravar todas as qualidades" na aba DVR (o disco cresce na mesma proporção).
            </p>
          </Panel>
        ) : (
          <Panel icon={Gauge} title="Perfil" sub="Perfis ficam em Configurações › Transcodificação.">
            <label className="field-label">
              Perfil se precisar transcodificar
              <select
                value={sel[0] ?? ""}
                disabled={!canWrite}
                onChange={(e) =>
                  ed.setDraft({ ...ed.draft, profiles: e.target.value ? [e.target.value] : [] })
                }
              >
                <option value="">Padrão do sistema</option>
                {all.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.id} · {x.video.width}×{x.video.height}
                  </option>
                ))}
              </select>
            </label>
            {ed.errs.profiles && <div className="prow-err">{ed.errs.profiles}</div>}
            <p className="aside-note">
              No modo automático o perfil só é usado quando a fonte não serve para HLS (MPEG-2, HEVC). Para
              várias qualidades, escolha Transcodificar.
            </p>
          </Panel>
        )}
      </aside>
      <div className="tab-foot">
        <ErrorBox error={ed.error} />
        <SaveBar
          {...ed}
          onReset={ed.reset}
          onSave={() =>
            void ed.save({ mode: ed.draft.mode, profiles: ed.draft.profiles, overlay: ed.draft.overlay })
          }
        />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- DVR

/** Longest interval the media node exports at once (server limit: 2 h). */
const MAX_EXPORT_MINUTES = 120;

/** datetime-local value (local time, minute precision) for an instant. */
function localInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/**
 * Downloads a recorded interval as an MP4. The media node copies the recorded
 * packets (no re-encoding); a hole in the recording ends the file there.
 */
function ExportMP4({ base, token }: { base: string; token: string | null }) {
  const [from, setFrom] = useState(() => localInput(new Date(Date.now() - 3600_000)));
  const [minutes, setMinutes] = useState(10);
  const start = Math.floor(new Date(from).getTime() / 1000);
  const valid = Number.isFinite(start) && minutes >= 1 && minutes <= MAX_EXPORT_MINUTES;
  const href = valid ? withToken(`${base}archive-${start}-${minutes * 60}.mp4`, token) : undefined;
  return (
    <Panel icon={Download} title="Exportar em MP4">
      <div className="form">
        <label>
          Início
          <input type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label>
          Duração (minutos, até {MAX_EXPORT_MINUTES})
          <input
            type="number"
            min={1}
            max={MAX_EXPORT_MINUTES}
            value={minutes}
            onChange={(e) => setMinutes(Number(e.target.value))}
          />
        </label>
        {href ? (
          <a className="btn" href={href} download>
            <Download size={16} /> Baixar MP4
          </a>
        ) : (
          <button disabled>
            <Download size={16} /> Baixar MP4
          </button>
        )}
      </div>
      <p className="aside-note">
        O servidor copia o que foi gravado, sem recodificar. Se houver um buraco na gravação, o arquivo
        termina nele.
      </p>
    </Panel>
  );
}

export function DVRTab({ c, st, canWrite, onSaved, onWatch, token }: TabProps) {
  const storages = useApi<Page<DVRStorage>>("/dvr/storages");
  const ed = useEditor<DVRConfig>(c, c.dvr, onSaved);
  const d = ed.draft;
  const where = d.storage ? `s:${d.storage}` : d.path ? "path" : "";
  const base = st.outputs?.dvr;
  const days = Math.max(1, Math.round((d.retention_hours || 72) / 24));
  const rate = st.input?.bitrate_bps ?? 0;
  const estimate = rate ? (rate / 8) * (d.retention_hours || 72) * 3600 : 0;
  const foot = (
    <div className="tab-foot">
      <ErrorBox error={ed.error} />
      <SaveBar {...ed} onReset={ed.reset} onSave={() => void ed.save({ dvr: d })} />
    </div>
  );

  if (!d.enabled)
    return (
      <>
        <Panel icon={Archive} title="Gravação (DVR)">
          <Feature
            icon={Archive}
            title="Este canal não está gravando"
            action={
              canWrite && (
                <button
                  onClick={() =>
                    ed.setDraft({ ...d, enabled: true, retention_hours: d.retention_hours || 72 })
                  }
                >
                  <Archive size={15} /> Ligar gravação
                </button>
              )
            }
          >
            Com o DVR ligado, o servidor de mídia grava o canal em arquivos de uma hora e oferece timeshift
            (voltar no tempo) e catch-up (assistir um programa que já passou), com links no formato do
            Flussonic.
          </Feature>
        </Panel>
        {foot}
      </>
    );

  return (
    <div className="tab-grid">
      <div>
        <Panel
          icon={Archive}
          title="Gravação (DVR)"
          sub="Gravado no servidor que roda o canal."
          flush
          actions={
            canWrite && (
              <Switch label="Gravação" checked onChange={() => ed.setDraft({ ...d, enabled: false })} />
            )
          }
        >
          <Row
            icon={Timer}
            title="Retenção"
            hint="Gravações mais antigas são apagadas."
            err={ed.errs["dvr.retention_hours"]}
          >
            <Num
              value={days}
              unit={days === 1 ? "dia" : "dias"}
              min={1}
              step={1}
              disabled={!canWrite}
              onChange={(n) => ed.setDraft({ ...d, retention_hours: Math.max(1, n) * 24 })}
            />
          </Row>
          {c.mode === "transcode" && c.profiles.length > 1 && (
            <Row
              icon={Layers}
              title="Qualidades gravadas"
              hint={`Este canal tem ${c.profiles.length} qualidades. Ligado, o catch-up oferece todas e o player escolhe pela banda, mas o disco cresce com a soma delas. Desligado, só a mais alta é gravada.`}
            >
              <Switch
                label="Gravar todas as qualidades"
                checked={!!d.all_qualities}
                disabled={!canWrite}
                onChange={(v) => ed.setDraft({ ...d, all_qualities: v })}
              />
            </Row>
          )}
          <Row
            icon={HardDrive}
            title="Onde gravar"
            hint="Pasta padrão do servidor, um armazenamento com vários discos (RAID) ou uma pasta própria."
            err={ed.errs["dvr.storage"] ?? ed.errs["dvr.path"]}
          >
            <select
              value={where}
              disabled={!canWrite}
              onChange={(e) => {
                const v = e.target.value;
                if (v.startsWith("s:")) ed.setDraft({ ...d, storage: v.slice(2), path: "" });
                else if (v === "path") ed.setDraft({ ...d, storage: "", path: d.path || "/storage/dvr" });
                else ed.setDraft({ ...d, storage: "", path: "" });
              }}
            >
              <option value="">Pasta padrão do servidor</option>
              {storages.data?.items.map((s) => (
                <option key={s.id} value={`s:${s.id}`}>
                  {s.id} · {s.mode === "single" ? "disco único" : s.mode.toUpperCase()}
                </option>
              ))}
              <option value="path">Pasta específica…</option>
            </select>
          </Row>
          {where === "path" && (
            <Row
              title="Pasta no servidor"
              hint="Caminho absoluto no servidor de mídia."
              err={ed.errs["dvr.path"]}
            >
              <input
                className="mono"
                value={d.path ?? ""}
                disabled={!canWrite}
                onChange={(e) => ed.setDraft({ ...d, path: e.target.value })}
              />
            </Row>
          )}
        </Panel>
        {c.dvr.enabled && base ? (
          <Recordings
            base={base}
            retentionHours={c.dvr.retention_hours}
            playingFrom={null}
            onPlay={(f) => onWatch(f)}
          />
        ) : (
          c.dvr.enabled && (
            <Panel icon={CalendarDays} title="Gravações">
              <Empty icon={CalendarDays}>
                As gravações aparecem aqui quando o canal estiver rodando em um servidor.
              </Empty>
            </Panel>
          )
        )}
      </div>
      <aside>
        <Panel icon={HardDrive} title="Espaço">
          <KV
            items={[
              ["Taxa atual", rate ? bitrate(rate) : "—"],
              ["Retenção", `${days} ${days === 1 ? "dia" : "dias"}`],
              ["Espaço estimado", estimate ? bytes(estimate) : "—"],
            ]}
          />
          <p className="aside-note">Estimativa pela taxa de agora; muda com a taxa real do canal.</p>
        </Panel>
        {base && (
          <Panel icon={Link2} title="Links">
            <CopyLink label="Timeshift (última hora)" value={withToken(`${base}index.m3u8`, token) ?? ""} />
            <CopyLink
              label="Catch-up · 1 h atrás, 1 h de duração"
              value={withToken(`${base}index-${Math.floor(Date.now() / 1000) - 3600}-3600.m3u8`, token) ?? ""}
            />
            <CopyLink
              label="Timeshift desde 2 h atrás"
              value={withToken(`${base}index-${Math.floor(Date.now() / 1000) - 7200}-now.m3u8`, token) ?? ""}
            />
          </Panel>
        )}
        {base && !c.drm?.enabled && <ExportMP4 base={base} token={token} />}
      </aside>
      {foot}
    </div>
  );
}

// ---------------------------------------------------------------- Output

export function OutputTab({ c, st, canWrite, onSaved, token }: TabProps) {
  const ed = useEditor<{ outputs: Outputs; drm: DRMConfig }>(
    c,
    { outputs: c.outputs, drm: c.drm ?? { enabled: false } },
    onSaved,
  );
  const { outputs: o, drm } = ed.draft;
  const set = (patch: Partial<Outputs>) => ed.setDraft({ ...ed.draft, outputs: { ...o, ...patch } });
  const ro = !canWrite;
  return (
    <div className="tab-grid">
      <div>
        <Panel icon={Tv} title="Saídas" sub="Como o canal é entregue aos players." flush>
          <Row
            icon={Tv}
            title="HLS"
            hint="Playlist .m3u8 com segmentos MPEG-TS. Toca em navegadores, celulares, Smart TVs e STBs."
          >
            <Switch label="HLS" checked={o.hls} disabled={ro} onChange={(b) => set({ hls: b })} />
          </Row>
          <Row
            icon={Film}
            title="MPEG-DASH"
            hint={
              drm.enabled
                ? "Indisponível com DRM: o DASH não é criptografado."
                : "Manifesto .mpd com fMP4, para players DASH e Smart TVs."
            }
            err={ed.errs["outputs.dash"]}
          >
            <Switch
              label="MPEG-DASH"
              checked={!!o.dash && !drm.enabled}
              disabled={ro || drm.enabled}
              onChange={(b) => set({ dash: b })}
            />
          </Row>
          <Row
            icon={Radio}
            title="MPEG-TS por HTTP"
            hint={
              drm.enabled
                ? "Indisponível com DRM: esse fluxo não é criptografado."
                : "Fluxo contínuo para STBs e VLC."
            }
            err={ed.errs["outputs.mpegts_http"]}
          >
            <Switch
              label="MPEG-TS por HTTP"
              checked={o.mpegts_http && !drm.enabled}
              disabled={ro || drm.enabled}
              onChange={(b) => set({ mpegts_http: b })}
            />
          </Row>
          <Row
            icon={Network}
            title="MPEG-TS por UDP"
            hint="Envio para a rede interna (multicast). Vazio = desligado."
            err={ed.errs["outputs.mpegts_udp"]}
          >
            <input
              className="mono"
              value={o.mpegts_udp ?? ""}
              placeholder="udp://239.10.0.1:5000"
              disabled={ro}
              onChange={(e) => set({ mpegts_udp: e.target.value || undefined })}
            />
          </Row>
        </Panel>

        <Panel
          icon={drm.enabled ? ShieldCheck : ShieldOff}
          title="Proteção (DRM)"
          sub="HLS e DVR criptografados com AES-128; a chave só é entregue com token."
          flush
          actions={
            <Switch
              label="DRM"
              checked={drm.enabled}
              disabled={ro}
              onChange={(b) =>
                ed.setDraft({
                  outputs: b ? { ...o, mpegts_http: false, dash: false } : o,
                  drm: {
                    enabled: b,
                    method: "aes-128",
                    key_rotation_minutes: drm.key_rotation_minutes || 60,
                  },
                })
              }
            />
          }
        >
          {drm.enabled ? (
            <>
              <Row
                icon={KeyRound}
                title="Trocar a chave a cada"
                hint="Uma chave vazada só abre esse período."
                err={ed.errs["drm.key_rotation_minutes"]}
              >
                <Num
                  value={drm.key_rotation_minutes ?? 60}
                  unit="min"
                  min={1}
                  max={1440}
                  step={1}
                  disabled={ro}
                  onChange={(n) => ed.setDraft({ ...ed.draft, drm: { ...drm, key_rotation_minutes: n } })}
                />
              </Row>
              <Row
                icon={Lock}
                title="Método"
                hint="Funciona em qualquer player HLS. DRM comercial (Widevine, PlayReady, FairPlay) depende de provedor."
              >
                <span className="tag accent">AES-128</span>
              </Row>
            </>
          ) : (
            <p className="panel-note">Desligado: qualquer pessoa com o link consegue assistir.</p>
          )}
        </Panel>
        <Panel
          icon={Send}
          title="Push"
          sub="Envia o canal para outros servidores (CDN, YouTube, outro media server). Cada destino reconecta sozinho."
          flush
          actions={
            !ro &&
            (o.push ?? []).length < 10 && (
              <button className="secondary sm" onClick={() => set({ push: [...(o.push ?? []), ""] })}>
                <Plus size={14} /> Destino
              </button>
            )
          }
        >
          {(o.push ?? []).length ? (
            (o.push ?? []).map((dst, i) => {
              const live = (st.pushes ?? [])[i];
              const err = ed.errs[`outputs.push[${i}]`];
              return (
                <div key={i} className={`push-row ${err ? "invalid" : ""}`}>
                  <span
                    className={`src-dot ${live?.state === "running" ? "on" : live?.state === "retrying" ? "bad" : "standby"}`}
                  />
                  <div className="push-main">
                    <input
                      className={`mono ${err ? "invalid" : ""}`}
                      value={dst}
                      placeholder="rtmp://a.rtmp.youtube.com/live2/CHAVE  ou  srt://cdn:9000"
                      disabled={ro}
                      onChange={(e) =>
                        set({ push: (o.push ?? []).map((x, j) => (j === i ? e.target.value : x)) })
                      }
                    />
                    <div className="src-meta">
                      {err ? (
                        <span className="bad">{err}</span>
                      ) : live ? (
                        <>
                          {PUSH_STATE[live.state] ?? live.state} {ago(live.since)} · {bytes(live.bytes)}{" "}
                          enviados
                          {live.restarts > 0 && ` · ${live.restarts} reconexões`}
                          {live.last_error && live.state !== "running" && (
                            <span className="bad"> · {live.last_error}</span>
                          )}
                        </>
                      ) : (
                        "salve para começar a enviar"
                      )}
                    </div>
                  </div>
                  {!ro && (
                    <button
                      className="icon"
                      title="Remover destino"
                      onClick={() => set({ push: (o.push ?? []).filter((_, j) => j !== i) })}
                    >
                      <Trash2 size={15} />
                    </button>
                  )}
                </div>
              );
            })
          ) : (
            <p className="panel-note">
              Nenhum destino. RTMP/RTMPS levam um vídeo e um áudio; SRT leva todas as trilhas.
            </p>
          )}
        </Panel>
      </div>
      <aside>
        <Panel
          icon={Link2}
          title="Links do canal"
          sub={token ? "Com o token de reprodução do painel." : "Copie para testar no VLC ou num player."}
        >
          {st.outputs && Object.keys(st.outputs).length ? (
            Object.entries(st.outputs).map(([k, v]) => (
              <CopyLink
                key={k}
                icon={k === "dvr" ? Archive : k === "mpegts_udp" ? Network : Tv}
                label={OUTPUT_LABEL[k] ?? k}
                value={k === "mpegts_udp" ? v : (withToken(k === "dvr" ? `${v}index.m3u8` : v, token) ?? v)}
              />
            ))
          ) : (
            <Empty icon={Link2}>Os links aparecem quando o canal estiver rodando.</Empty>
          )}
        </Panel>
      </aside>
      <div className="tab-foot">
        <ErrorBox error={ed.error} />
        <SaveBar
          {...ed}
          onReset={ed.reset}
          onSave={() =>
            void ed.save({
              outputs: {
                ...o,
                mpegts_http: o.mpegts_http && !drm.enabled,
                dash: !!o.dash && !drm.enabled,
                mpegts_udp: o.mpegts_udp || undefined,
                push: (o.push ?? []).map((x) => x.trim()).filter(Boolean),
              },
              drm: drm.enabled ? drm : { enabled: false },
            })
          }
        />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- EPG

export function EPGTab({ c, canWrite, onSaved, onWatch }: TabProps) {
  const ed = useEditor<EPGConfig>(c, c.epg ?? {}, onSaved);
  const [day, setDay] = useState(0);
  const range = useMemo(() => {
    const d = new Date();
    d.setHours(0, 0, 0, 0);
    d.setDate(d.getDate() + day);
    const to = new Date(d);
    to.setDate(to.getDate() + 1);
    return { from: d.toISOString(), to: to.toISOString() };
  }, [day]);
  const guide = useApi<{ programs: Program[]; status?: EPGStatus }>(
    c.epg?.url
      ? `/channels/${c.id}/epg?from=${encodeURIComponent(range.from)}&to=${encodeURIComponent(range.to)}`
      : null,
    60000,
  );
  const [refreshing, setRefreshing] = useState(false);
  const [refreshError, setRefreshError] = useState<unknown>(null);
  const refresh = async () => {
    setRefreshing(true);
    setRefreshError(null);
    try {
      await api.post(`/channels/${c.id}/epg/refresh`);
      void guide.reload();
    } catch (e) {
      setRefreshError(e);
    } finally {
      setRefreshing(false);
    }
  };
  const now = Date.now();
  const status = guide.data?.status;
  const programs = guide.data?.programs ?? [];
  const current = programs.find(
    (p) => new Date(p.start).getTime() <= now && now < new Date(p.stop).getTime(),
  );
  const next = programs.find((p) => new Date(p.start).getTime() > now);

  const source = (
    <Panel
      icon={Globe}
      title="Fonte do guia"
      sub="Arquivo XMLTV (.xml ou .xml.gz). Vários canais podem usar o mesmo."
      flush
    >
      <Row title="URL do XMLTV" stack err={ed.errs["epg.url"]}>
        <input
          className="mono"
          value={ed.draft.url ?? ""}
          placeholder="https://epg.exemplo.com/guia.xml.gz"
          disabled={!canWrite}
          onChange={(e) => ed.setDraft({ ...ed.draft, url: e.target.value })}
        />
      </Row>
      <Row title="ID do canal no XMLTV" hint={`Vazio = ${c.id}`} stack err={ed.errs["epg.xmltv_id"]}>
        <input
          className="mono"
          value={ed.draft.xmltv_id ?? ""}
          placeholder={c.id}
          disabled={!canWrite}
          onChange={(e) => ed.setDraft({ ...ed.draft, xmltv_id: e.target.value })}
        />
      </Row>
      {c.epg?.url && (
        <Row
          title="Última importação"
          hint={
            status?.error ? (
              <span className="bad">{status.error}</span>
            ) : status ? (
              `${status.programs} programas · ${ago(status.fetched_at)} · atualiza a cada 6 h`
            ) : (
              "ainda não importado"
            )
          }
        >
          {canWrite && !ed.dirty && (
            <button className="secondary sm" onClick={() => void refresh()} disabled={refreshing}>
              <RefreshCw size={13} className={refreshing ? "spin" : ""} />{" "}
              {refreshing ? "Importando…" : "Importar"}
            </button>
          )}
        </Row>
      )}
    </Panel>
  );
  const foot = (
    <div className="tab-foot">
      <ErrorBox error={ed.error ?? refreshError} />
      <SaveBar
        {...ed}
        onReset={ed.reset}
        onSave={() => void ed.save({ epg: { url: ed.draft.url ?? "", xmltv_id: ed.draft.xmltv_id ?? "" } })}
      />
    </div>
  );

  if (!c.epg?.url)
    return (
      <div className="tab-grid">
        <Panel icon={CalendarDays} title="Programação (EPG)">
          <Feature icon={CalendarDays} title="Sem guia de programação">
            Aponte um arquivo XMLTV e o sistema importa a programação deste canal a cada 6 horas. Com o DVR
            ligado, os programas que já passaram podem ser assistidos de novo (catch-up). O guia de todos os
            canais fica em <code>/api/v1/epg.xml</code> para os players.
          </Feature>
        </Panel>
        <aside>{source}</aside>
        {foot}
      </div>
    );

  return (
    <div className="tab-grid">
      <div>
        <Panel
          icon={CalendarDays}
          title="Programação"
          flush
          actions={
            <div className="seg">
              {[-1, 0, 1].map((d) => (
                <button key={d} className={day === d ? "on" : ""} onClick={() => setDay(d)}>
                  {d === -1 ? "Ontem" : d === 0 ? "Hoje" : "Amanhã"}
                </button>
              ))}
            </div>
          }
        >
          <ErrorBox error={guide.error} />
          {programs.length ? (
            <div className="epg-list">
              {programs.map((p) => {
                const start = new Date(p.start).getTime();
                const stop = new Date(p.stop).getTime();
                const onAir = start <= now && now < stop;
                const past = stop <= now;
                return (
                  <div key={p.start} className={`epg-item ${onAir ? "on-air" : ""} ${past ? "past" : ""}`}>
                    <span className="epg-time mono">
                      {hhmm(p.start)}
                      <small>{hhmm(p.stop)}</small>
                    </span>
                    <div className="epg-body">
                      <div className="epg-title">
                        {p.title}
                        {onAir && <span className="tag accent">no ar</span>}
                        {p.category && <span className="tag">{p.category}</span>}
                      </div>
                      {p.description && <div className="epg-desc">{p.description}</div>}
                      {onAir && (
                        <div className="epg-progress">
                          <span style={{ width: `${((now - start) / (stop - start)) * 100}%` }} />
                        </div>
                      )}
                    </div>
                    {c.dvr.enabled && (past || onAir) && (
                      <button
                        className="secondary sm"
                        onClick={() => onWatch(unix(p.start), past ? unix(p.stop) : undefined)}
                        title="Assistir pelo DVR (catch-up)"
                      >
                        Assistir
                      </button>
                    )}
                  </div>
                );
              })}
            </div>
          ) : (
            <Empty icon={CalendarDays}>
              Nenhum programa neste dia. Importe o guia ou confira o ID do canal no XMLTV.
            </Empty>
          )}
        </Panel>
      </div>
      <aside>
        {(current || next) && day === 0 && (
          <Panel icon={Tv} title="Agora">
            {current && (
              <div className="now-card">
                <span className="tag accent">no ar · até {hhmm(current.stop)}</span>
                <strong>{current.title}</strong>
              </div>
            )}
            {next && (
              <div className="now-card">
                <span className="tag">a seguir · {hhmm(next.start)}</span>
                <strong>{next.title}</strong>
              </div>
            )}
          </Panel>
        )}
        {source}
      </aside>
      {foot}
    </div>
  );
}

// ---------------------------------------------------------------- Auth

export function AuthTab({ c, canWrite, onSaved }: TabProps) {
  const ed = useEditor<AuthConfig>(c, c.auth ?? { enabled: false }, onSaved);
  const a = ed.draft;
  const lines = (v?: string[]) => (v ?? []).join("\n");
  const parse = (v: string) =>
    v
      .split(/[\n,]/)
      .map((x) => x.trim())
      .filter(Boolean);
  const [allow, setAllow] = useState(lines(a.allow_ips));
  const [deny, setDeny] = useState(lines(a.deny_ips));
  const ro = !canWrite;
  const foot = (
    <div className="tab-foot">
      <ErrorBox error={ed.error} />
      <SaveBar
        {...ed}
        onReset={() => {
          ed.reset();
          setAllow(lines(c.auth?.allow_ips));
          setDeny(lines(c.auth?.deny_ips));
        }}
        onSave={() => void ed.save({ auth: a.enabled ? a : { enabled: false } })}
      />
    </div>
  );
  const integration = (
    <Panel icon={ListChecks} title="Integração com o middleware">
      <p className="aside-note" style={{ marginTop: 0 }}>
        A cada nova sessão o servidor de mídia chama o backend. Resposta 2xx libera; qualquer outra nega.
      </p>
      <pre className="code-block">{`GET <backend>?name=${c.id}
  &ip=…&token=…&proto=hls
  &user_agent=…&session_id=…`}</pre>
      <KV
        items={[
          [<code key="a">X-AuthDuration</code>, "segundos até consultar de novo"],
          [<code key="b">X-UserId</code>, "identifica o assinante"],
          [<code key="c">X-Max-Sessions</code>, "limite do assinante"],
          [<code key="d">X-Unique: true</code>, "derruba as outras sessões"],
        ]}
      />
      <p className="aside-note">
        O token vem do link do player (…/index.m3u8?token=…) e é repetido em todos os segmentos. Tokens
        assinados pelo próprio sistema liberam sem consultar o backend.
      </p>
    </Panel>
  );

  if (!a.enabled)
    return (
      <div className="tab-grid">
        <Panel icon={ShieldOff} title="Autorização de reprodução">
          <Feature
            icon={ShieldOff}
            title="Acesso livre"
            action={
              canWrite && (
                <button onClick={() => ed.setDraft({ ...a, enabled: true })}>
                  <ShieldCheck size={15} /> Controlar acesso
                </button>
              )
            }
          >
            Qualquer pessoa com o link assiste. Ligue o controle para validar cada espectador no seu
            middleware (como o on_play do Flussonic), liberar ou bloquear redes e limitar o número de sessões.
          </Feature>
        </Panel>
        <aside>{integration}</aside>
        {foot}
      </div>
    );

  return (
    <div className="tab-grid">
      <div>
        <Panel
          icon={ShieldCheck}
          title="Autorização de reprodução"
          sub="Aplicada pelo servidor de mídia a cada nova sessão."
          flush
          actions={
            <Switch
              label="Controle de acesso"
              checked
              disabled={ro}
              onChange={() => ed.setDraft({ ...a, enabled: false })}
            />
          }
        >
          <Row
            icon={Server}
            title="Backend de autorização"
            hint="Vazio = só as regras desta página."
            stack
            err={ed.errs["auth.backend_url"]}
          >
            <input
              className="mono"
              value={a.backend_url ?? ""}
              placeholder="https://middleware.exemplo/auth"
              disabled={ro}
              onChange={(e) => ed.setDraft({ ...a, backend_url: e.target.value })}
            />
          </Row>
          <Row
            icon={Timer}
            title="Guardar a resposta por"
            hint="Depois disso o backend é consultado de novo."
            err={ed.errs["auth.cache_seconds"]}
          >
            <Num
              value={a.cache_seconds || 60}
              unit="s"
              min={0}
              step={1}
              disabled={ro}
              onChange={(n) => ed.setDraft({ ...a, cache_seconds: n })}
            />
          </Row>
          <Row icon={XCircle} title="Se o backend não responder">
            <select
              value={a.allow_on_error ? "allow" : "deny"}
              disabled={ro}
              onChange={(e) => ed.setDraft({ ...a, allow_on_error: e.target.value === "allow" })}
            >
              <option value="deny">Negar (mais seguro)</option>
              <option value="allow">Liberar (mantém no ar)</option>
            </select>
          </Row>
          <Row
            icon={Users}
            title="Máximo de sessões"
            hint="Neste canal, por servidor. 0 = sem limite."
            err={ed.errs["auth.max_sessions"]}
          >
            <Num
              value={a.max_sessions ?? 0}
              min={0}
              step={1}
              disabled={ro}
              onChange={(n) => ed.setDraft({ ...a, max_sessions: n })}
            />
          </Row>
        </Panel>
        <Panel
          icon={Network}
          title="Redes"
          sub="Um IP ou rede por linha (ex.: 10.0.0.0/8). Valem em todo pedido, até com token."
        >
          <div className="ip-lists">
            <label className="field-label">
              <span>
                Liberadas <span className="muted">· vazio = todas</span>
              </span>
              <textarea
                className={`mono ${ed.errs["auth.allow_ips"] ? "invalid" : ""}`}
                rows={5}
                value={allow}
                disabled={ro}
                placeholder="10.0.0.0/8"
                onChange={(e) => {
                  setAllow(e.target.value);
                  ed.setDraft({ ...a, allow_ips: parse(e.target.value) });
                }}
              />
              {ed.errs["auth.allow_ips"] && <span className="prow-err">{ed.errs["auth.allow_ips"]}</span>}
            </label>
            <label className="field-label">
              <span>
                Bloqueadas <span className="muted">· valem antes de tudo</span>
              </span>
              <textarea
                className={`mono ${ed.errs["auth.deny_ips"] ? "invalid" : ""}`}
                rows={5}
                value={deny}
                disabled={ro}
                placeholder="10.9.0.0/16"
                onChange={(e) => {
                  setDeny(e.target.value);
                  ed.setDraft({ ...a, deny_ips: parse(e.target.value) });
                }}
              />
              {ed.errs["auth.deny_ips"] && <span className="prow-err">{ed.errs["auth.deny_ips"]}</span>}
            </label>
          </div>
        </Panel>
      </div>
      <aside>{integration}</aside>
      {foot}
    </div>
  );
}

// ---------------------------------------------------------------- Play sessions

const PROTO: Record<string, string> = { hls: "HLS", mpegts: "MPEG-TS", dvr: "DVR" };

export function SessionsTab({ c, canOperate }: TabProps) {
  const data = useApi<{ open: PlaySession[]; closed: PlaySession[]; worker_id: string }>(
    `/channels/${c.id}/sessions`,
    5000,
  );
  const [error, setError] = useState<unknown>(null);
  const open = data.data?.open ?? [];
  const rate = (s: PlaySession) => {
    const secs = (new Date(s.last_seen).getTime() - new Date(s.started_at).getTime()) / 1000;
    return secs > 5 ? (s.bytes * 8) / secs : 0;
  };
  const total = open.reduce((n, s) => n + rate(s), 0);
  const users = new Set(open.map((s) => s.user_id || s.ip)).size;
  const kill = async (s: PlaySession) => {
    if (!confirm(`Encerrar a sessão de ${s.user_id || s.ip}? Ela fica bloqueada por 2 minutos.`)) return;
    setError(null);
    try {
      await api.del(`/channels/${c.id}/sessions/${s.id}`);
      void data.reload();
    } catch (e) {
      setError(e);
    }
  };
  const stats: [typeof Users, string, ReactNode][] = [
    [Users, "Sessões abertas", open.length],
    [UserCheck, "Espectadores únicos", users],
    [Gauge, "Banda estimada", total ? bitrate(total) : "—"],
    [Server, "Servidor", data.data?.worker_id || "—"],
  ];
  return (
    <>
      <div className="stat-strip">
        {stats.map(([Icon, label, value]) => (
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
      <Panel icon={Users} title="Assistindo agora" sub="Atualiza a cada 5 s." flush>
        {open.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Espectador</th>
                  <th>Protocolo</th>
                  <th>Player</th>
                  <th>Início</th>
                  <th>Duração</th>
                  <th>Dados</th>
                  <th>Taxa</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {open.map((s) => (
                  <tr key={s.id}>
                    <td>
                      <div className="mono">{s.ip}</div>
                      {(s.user_id || s.token) && (
                        <div className="small muted">
                          {s.user_id ? `assinante ${s.user_id}` : `token ${s.token}`}
                        </div>
                      )}
                    </td>
                    <td>
                      <span className="tag">{PROTO[s.proto] ?? s.proto}</span>
                    </td>
                    <td className="truncate" style={{ maxWidth: 240 }} title={s.user_agent}>
                      {s.user_agent || "—"}
                    </td>
                    <td title={dateTime(s.started_at)}>{hhmm(s.started_at)}</td>
                    <td>{duration(s.started_at)}</td>
                    <td>{bytes(s.bytes)}</td>
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
      <SessionHistory channelId={c.id} />
    </>
  );
}
