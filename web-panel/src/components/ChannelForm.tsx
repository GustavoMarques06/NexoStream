import { ChevronRight, Plus, X } from "lucide-react";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { api } from "../api";
import { useApi } from "../hooks";
import type {
  Channel,
  ChannelDefaults,
  DRMConfig,
  DVRStorage,
  Failover,
  Mode,
  Page,
  Profile,
  Source,
} from "../types";
import { ErrorBox } from "./ui";

const DEFAULT_FAILOVER: Failover = {
  no_packets_seconds: 3,
  confirm_window_seconds: 5,
  low_bitrate_ratio: 0.3,
  low_bitrate_seconds: 10,
  missing_stream_seconds: 5,
  return_to_primary: true,
  primary_stable_seconds: 60,
};

const ROTATIONS = [
  { v: 5, label: "5 min" },
  { v: 15, label: "15 min" },
  { v: 30, label: "30 min" },
  { v: 60, label: "1 hora" },
  { v: 120, label: "2 horas" },
  { v: 360, label: "6 horas" },
  { v: 1440, label: "24 horas" },
];

const MODES: { id: Mode; title: string; hint: string }[] = [
  { id: "auto", title: "Automático", hint: "Repassa quando compatível" },
  { id: "passthrough", title: "Passthrough", hint: "Sem recodificar" },
  { id: "transcode", title: "Transcodificar", hint: "Usa o perfil escolhido" },
];

interface Props {
  channel?: Channel;
  onSaved: (c: Channel) => void;
  onCancel: () => void;
}

export function ChannelForm({ channel, onSaved, onCancel }: Props) {
  const editing = !!channel;
  const profiles = useApi<Page<Profile>>("/transcode-profiles");
  const storages = useApi<Page<DVRStorage>>("/dvr/storages");
  const [id, setId] = useState(channel?.id ?? "");
  const [name, setName] = useState(channel?.name ?? "");
  const [group, setGroup] = useState(channel?.group ?? "");
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [mode, setMode] = useState<Mode>(channel?.mode ?? "auto");
  const [profile, setProfile] = useState(channel?.profiles[0] ?? "");
  const [sources, setSources] = useState<Source[]>(
    channel?.sources.length ? channel.sources : [{ priority: 1, url: "", interface: "" }],
  );
  const [hls, setHls] = useState(channel?.outputs.hls ?? true);
  const [tsHttp, setTsHttp] = useState(channel?.outputs.mpegts_http ?? false);
  const [udpOut, setUdpOut] = useState(channel?.outputs.mpegts_udp ?? "");
  const [dvr, setDvr] = useState(channel?.dvr ?? { enabled: false, retention_hours: 72 });
  const [drm, setDrm] = useState<DRMConfig>(channel?.drm ?? { enabled: false });
  const [fo, setFo] = useState<Failover>(channel?.failover ?? DEFAULT_FAILOVER);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);

  // New channels start with the defaults from the Settings page.
  const defaults = useApi<ChannelDefaults>(editing ? null : "/settings/defaults");
  const seeded = useRef(false);
  useEffect(() => {
    const d = defaults.data;
    if (!d || seeded.current) return;
    seeded.current = true;
    setFo(d.default_failover);
    setDvr((v) => ({
      ...v,
      retention_hours: d.dvr_default_retention_hours,
      storage: v.storage || d.dvr_default_storage || undefined,
    }));
  }, [defaults.data]);
  const defaultRetention = defaults.data?.dvr_default_retention_hours ?? 72;

  const setSource = (i: number, patch: Partial<Source>) =>
    setSources((s) => s.map((x, j) => (j === i ? { ...x, ...patch } : x)));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);
    const body = {
      ...(editing ? {} : id ? { id } : {}),
      name,
      group,
      enabled,
      mode,
      // The modal edits the first profile; the rest of an ABR ladder is kept.
      profiles: profile
        ? mode === "transcode"
          ? [profile, ...(channel?.profiles ?? []).filter((x, i) => i > 0 && x !== profile)]
          : [profile]
        : [],
      sources: sources.map((s, i) => ({ ...s, priority: i + 1, interface: s.interface || undefined })),
      outputs: { hls, mpegts_http: tsHttp && !drm.enabled, mpegts_udp: udpOut || undefined },
      dvr,
      drm: drm.enabled
        ? { enabled: true, method: "aes-128", key_rotation_minutes: drm.key_rotation_minutes || 60 }
        : { enabled: false },
      failover: fo,
    };
    try {
      onSaved(
        editing
          ? await api.patch<Channel>(`/channels/${channel.id}`, body)
          : await api.post<Channel>("/channels", body),
      );
    } catch (err) {
      setError(err);
    } finally {
      setSaving(false);
    }
  };

  const num = (k: keyof Failover, label: string) => (
    <label>
      {label}
      <input
        type="number"
        step="any"
        value={fo[k] as number}
        onChange={(e) => setFo({ ...fo, [k]: Number(e.target.value) })}
      />
    </label>
  );

  return (
    <form onSubmit={submit} className="form">
      <ErrorBox error={error} />

      <div className="form-section">
        <div className="fields">
          <label>
            Nome
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              maxLength={200}
              autoFocus
            />
          </label>
          <label>
            Grupo
            <input value={group} onChange={(e) => setGroup(e.target.value)} placeholder="Opcional" />
          </label>
          {!editing && (
            <label>
              Identificador
              <input
                value={id}
                onChange={(e) => setId(e.target.value)}
                placeholder="automático"
                pattern="[a-z0-9][a-z0-9-]{0,63}"
                title="Letras minúsculas, números e hífen"
              />
            </label>
          )}
        </div>
      </div>

      <div className="form-section">
        <div className="form-section-title">Fontes</div>
        {sources.map((s, i) => (
          <div className="source-row" key={i}>
            <span className="prio" title={i === 0 ? "Principal" : "Backup"}>
              {i + 1}
            </span>
            <input
              value={s.url}
              onChange={(e) => setSource(i, { url: e.target.value })}
              placeholder="udp://239.1.1.1:1234"
              required
              aria-label="URL"
              className="mono"
            />
            <input
              className="iface"
              value={s.interface ?? ""}
              onChange={(e) => setSource(i, { interface: e.target.value })}
              placeholder="Interface"
              aria-label="Interface"
            />
            <button
              type="button"
              className="icon"
              disabled={sources.length === 1}
              onClick={() => setSources((x) => x.filter((_, j) => j !== i))}
              aria-label="Remover fonte"
            >
              <X size={15} />
            </button>
          </div>
        ))}
        <div>
          <button
            type="button"
            className="ghost sm"
            onClick={() => setSources((x) => [...x, { priority: x.length + 1, url: "", interface: "" }])}
          >
            <Plus size={14} /> Adicionar backup
          </button>
        </div>
      </div>

      <div className="form-section">
        <div className="form-section-title">Processamento</div>
        <div className="mode-picker">
          {MODES.map((m) => (
            <button
              type="button"
              key={m.id}
              className={`mode-option ${mode === m.id ? "on" : ""}`}
              onClick={() => setMode(m.id)}
            >
              {m.title}
              <span>{m.hint}</span>
            </button>
          ))}
        </div>
        {mode === "transcode" && (
          <label>
            Perfil de saída
            <select value={profile} onChange={(e) => setProfile(e.target.value)} required>
              <option value="">Selecione</option>
              {profiles.data?.items.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id} · {p.video.height}p · {(p.video.bitrate_bps / 1e6).toFixed(1)} Mbps
                </option>
              ))}
            </select>
          </label>
        )}
      </div>

      <div className="form-section">
        <div className="form-section-title">Saídas</div>
        <div className="toggles">
          <label className="check">
            <input type="checkbox" checked={hls} onChange={(e) => setHls(e.target.checked)} /> HLS
          </label>
          <label
            className="check"
            title={drm.enabled ? "Indisponível com DRM: o MPEG-TS por HTTP não é criptografado" : undefined}
          >
            <input
              type="checkbox"
              checked={tsHttp && !drm.enabled}
              disabled={drm.enabled}
              onChange={(e) => setTsHttp(e.target.checked)}
            />{" "}
            MPEG-TS HTTP
          </label>
          <label className="check">
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> Iniciar
            ao salvar
          </label>
        </div>
      </div>

      <div className="form-section">
        <div className="form-section-title">Gravação (DVR)</div>
        <div className="toggles">
          <label className="check">
            <input
              type="checkbox"
              checked={dvr.enabled}
              onChange={(e) =>
                setDvr({
                  ...dvr,
                  enabled: e.target.checked,
                  retention_hours: dvr.retention_hours || defaultRetention,
                })
              }
            />
            Gravar este canal (timeshift e catch-up)
          </label>
        </div>
        {dvr.enabled && (
          <div className="fields">
            <label>
              Retenção (dias)
              <input
                type="number"
                min={1}
                step={1}
                value={Math.max(1, Math.round((dvr.retention_hours || defaultRetention) / 24))}
                onChange={(e) =>
                  setDvr({ ...dvr, retention_hours: Math.max(1, Number(e.target.value)) * 24 })
                }
              />
            </label>
            <label>
              Onde gravar
              <select
                value={dvr.storage ? `s:${dvr.storage}` : dvr.path ? "path" : ""}
                onChange={(e) => {
                  const v = e.target.value;
                  if (v.startsWith("s:")) setDvr({ ...dvr, storage: v.slice(2), path: "" });
                  else if (v === "path") setDvr({ ...dvr, storage: "", path: dvr.path || "/storage/dvr" });
                  else setDvr({ ...dvr, storage: "", path: "" });
                }}
              >
                <option value="">Pasta padrão do servidor</option>
                {storages.data?.items.map((s) => (
                  <option key={s.id} value={`s:${s.id}`}>
                    {s.id} · {s.mode === "single" ? "disco único" : s.mode.toUpperCase()} · {s.disks.length}{" "}
                    disco(s)
                  </option>
                ))}
                <option value="path">Pasta específica…</option>
              </select>
            </label>
            {!dvr.storage && !!dvr.path && (
              <label>
                Pasta no servidor
                <input
                  className="mono"
                  value={dvr.path}
                  onChange={(e) => setDvr({ ...dvr, path: e.target.value })}
                />
              </label>
            )}
          </div>
        )}
      </div>

      <div className="form-section">
        <div className="form-section-title">Proteção (DRM)</div>
        <div className="toggles">
          <label className="check">
            <input
              type="checkbox"
              checked={drm.enabled}
              onChange={(e) => {
                setDrm({
                  ...drm,
                  enabled: e.target.checked,
                  key_rotation_minutes: drm.key_rotation_minutes || 60,
                });
                if (e.target.checked) setTsHttp(false);
              }}
            />
            Criptografar com AES-128 (HLS e DVR)
          </label>
        </div>
        {drm.enabled && (
          <>
            <div className="fields">
              <label>
                Trocar a chave a cada
                <select
                  value={drm.key_rotation_minutes || 60}
                  onChange={(e) => setDrm({ ...drm, key_rotation_minutes: Number(e.target.value) })}
                >
                  {!!drm.key_rotation_minutes && !ROTATIONS.some((m) => m.v === drm.key_rotation_minutes) && (
                    <option value={drm.key_rotation_minutes}>{drm.key_rotation_minutes} min</option>
                  )}
                  {ROTATIONS.map((m) => (
                    <option key={m.v} value={m.v}>
                      {m.label}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <p className="small muted" style={{ margin: "8px 0 0" }}>
              Os players precisam de um token de reprodução no link (…/index.m3u8?token=…). O painel gera o
              token sozinho; o middleware da operadora pode assiná-lo com a mesma chave. A saída MPEG-TS por
              HTTP fica desativada.
            </p>
          </>
        )}
      </div>

      <details className="advanced">
        <summary>
          <ChevronRight size={14} /> Avançado
        </summary>
        <div className="form-section">
          <div className="fields">
            {mode !== "transcode" && (
              <label>
                Perfil se precisar transcodificar
                <select value={profile} onChange={(e) => setProfile(e.target.value)}>
                  <option value="">Padrão</option>
                  {profiles.data?.items.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.id}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <label>
              MPEG-TS UDP
              <input
                value={udpOut}
                onChange={(e) => setUdpOut(e.target.value)}
                placeholder="udp://239.10.0.1:5000"
              />
            </label>
            {num("no_packets_seconds", "Sem pacotes (s)")}
            {num("confirm_window_seconds", "Confirmação (s)")}
            {num("primary_stable_seconds", "Retorno à principal (s)")}
            {num("missing_stream_seconds", "Sem áudio/vídeo (s)")}
          </div>
          <div className="toggles">
            <label className="check">
              <input
                type="checkbox"
                checked={fo.return_to_primary}
                onChange={(e) => setFo({ ...fo, return_to_primary: e.target.checked })}
              />
              Voltar à principal automaticamente
            </label>
          </div>
        </div>
      </details>

      <div className="form-actions">
        <button type="button" className="secondary" onClick={onCancel}>
          Cancelar
        </button>
        <button type="submit" disabled={saving}>
          {saving ? "Salvando…" : editing ? "Salvar" : "Criar canal"}
        </button>
      </div>
    </form>
  );
}
