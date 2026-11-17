import {
  Archive,
  Check,
  FileCode2,
  Gauge,
  Info,
  Lock,
  Network,
  RotateCcw,
  Save,
  KeyRound,
  Server,
  Settings2,
  ShieldCheck,
  SlidersHorizontal,
  Tv,
  Users,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api";
import { Embedded, Empty, ErrorBox, PageHeader, WorkerChip } from "../components/ui";
import { ago } from "../format";
import { useApi } from "../hooks";
import { useCan, useMe } from "../me";
import type {
  DVRStorage,
  Failover,
  Page,
  Profile,
  ServerSettings,
  ServerSettingsView,
  Settings as SettingsT,
  SettingsView,
  Worker,
} from "../types";
import { Audit } from "./Audit";
import { Config } from "./Config";
import { BrandingSection } from "./BrandingSection";
import { loadBranding } from "../branding";
import { Field, fieldErrors, NumInput, Section, type FieldErrors } from "../components/form";
import { Profiles } from "./Profiles";
import { Tokens } from "./Tokens";
import { Users as UsersPage } from "./Users";
import { DVRStorages } from "./DVR";
import { WorkerSummary } from "./Workers";

type TabId =
  | "general"
  | "ingest"
  | "hls"
  | "profiles"
  | "dvr"
  | "servers"
  | "cluster"
  | "security"
  | "users"
  | "tokens"
  | "audit"
  | "editor";

/** Each tab is shown to whoever has one of its scopes (none = everyone). */
const TABS: { id: TabId; label: string; icon: LucideIcon; scopes: string[]; group: string }[] = [
  { id: "general", label: "Geral", icon: Settings2, scopes: ["settings:admin"], group: "Sistema" },
  { id: "ingest", label: "Entrada", icon: Network, scopes: ["settings:admin"], group: "Sistema" },
  { id: "hls", label: "Saídas HLS", icon: Tv, scopes: ["settings:admin"], group: "Sistema" },
  {
    id: "profiles",
    label: "Transcodificação",
    icon: SlidersHorizontal,
    scopes: ["channels:read"],
    group: "Sistema",
  },
  { id: "dvr", label: "DVR", icon: Archive, scopes: ["settings:admin", "dvr:read"], group: "Infraestrutura" },
  {
    id: "servers",
    label: "Servidores",
    icon: Server,
    scopes: ["settings:admin", "workers:read"],
    group: "Infraestrutura",
  },
  { id: "cluster", label: "Cluster", icon: Gauge, scopes: ["settings:admin"], group: "Infraestrutura" },
  { id: "security", label: "Segurança", icon: Lock, scopes: ["settings:admin"], group: "Acesso" },
  { id: "users", label: "Usuários", icon: Users, scopes: ["users:admin"], group: "Acesso" },
  { id: "tokens", label: "API e tokens", icon: KeyRound, scopes: ["tokens:admin"], group: "Acesso" },
  { id: "audit", label: "Auditoria", icon: ShieldCheck, scopes: ["audit:read"], group: "Acesso" },
  { id: "editor", label: "Editor de texto", icon: FileCode2, scopes: [], group: "Avançado" },
];

/** Fields of each tab, for "restore defaults". */
const TAB_KEYS: Partial<Record<TabId, (keyof SettingsT)[]>> = {
  general: [
    "system_name",
    "brand_tagline",
    "brand_color",
    "brand_logo",
    "brand_login_message",
    "default_profile",
    "allow_hevc_passthrough",
  ],
  ingest: ["udp_read_buffer_bytes", "default_failover"],
  hls: ["hls_segment_seconds", "hls_list_size"],
  dvr: ["dvr_default_retention_hours", "dvr_default_storage"],
  cluster: [
    "scheduler_max_cpu_percent",
    "cost_passthrough",
    "cost_transcode_per_megapixel",
    "cost_gpu_offload",
    "worker_offline_seconds",
    "drain_migrations_per_pass",
  ],
  security: ["access_token_minutes", "refresh_token_hours", "login_attempts_per_minute"],
};

export function Settings() {
  const isAdmin = useCan("settings:admin");
  const me = useMe();
  const [params, setParams] = useSearchParams();
  const tabs = TABS.filter((t) => !t.scopes.length || t.scopes.some((sc) => me?.scopes.includes(sc)));
  const requested = params.get("tab") as TabId | null;
  const tab: TabId = tabs.find((t) => t.id === requested)?.id ?? tabs[0]?.id ?? "editor";
  const setTab = (id: TabId) => setParams(id === "general" ? {} : { tab: id }, { replace: true });

  const view = useApi<SettingsView>(isAdmin ? "/settings" : null);
  const [draft, setDraft] = useState<SettingsT | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [savedAt, setSavedAt] = useState(0);

  useEffect(() => {
    if (view.data && !draft) setDraft(view.data.settings);
  }, [view.data, draft]);

  const dirty = useMemo(
    () => !!draft && !!view.data && JSON.stringify(draft) !== JSON.stringify(view.data.settings),
    [draft, view.data],
  );
  const errs = fieldErrors(error);

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    setError(null);
    try {
      const res = await api.put<SettingsView>("/settings", draft);
      view.setData(res);
      setDraft(res.settings);
      setSavedAt(Date.now());
      void loadBranding();
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  const discard = () => {
    setDraft(view.data?.settings ?? null);
    setError(null);
  };

  const restoreTab = () => {
    const keys = TAB_KEYS[tab];
    if (!keys || !draft || !view.data) return;
    const next = { ...draft } as Record<string, unknown>;
    const defs = view.data.defaults as unknown as Record<string, unknown>;
    for (const k of keys) next[k] = defs[k];
    setDraft(next as unknown as SettingsT);
  };

  const sub = !isAdmin
    ? "Gravação, servidores e configuração em texto"
    : view.data?.saved
      ? `Alterado por ${view.data.settings.updated_by || "—"} ${ago(view.data.settings.updated_at)}`
      : "Usando os valores dos arquivos .env. Ao salvar, o painel passa a gerenciá-los.";

  return (
    <>
      <PageHeader title={`Configurações · ${TABS.find((t) => t.id === tab)?.label ?? ""}`} sub={sub}>
        {TAB_KEYS[tab] && draft && (
          <button className="secondary" onClick={restoreTab} title="Volta os campos desta aba ao padrão">
            <RotateCcw size={15} /> Padrões da aba
          </button>
        )}
      </PageHeader>

      <div className="settings-layout">
        <nav className="settings-nav" aria-label="Seções">
          {[...new Set(tabs.map((t) => t.group))].map((g) => (
            <div key={g} className="settings-nav-group">
              <div className="settings-nav-label">{g}</div>
              {tabs
                .filter((t) => t.group === g)
                .map((t) => (
                  <button
                    key={t.id}
                    aria-current={tab === t.id ? "page" : undefined}
                    className={tab === t.id ? "on" : ""}
                    onClick={() => setTab(t.id)}
                  >
                    <t.icon size={15} />
                    {t.label}
                  </button>
                ))}
            </div>
          ))}
        </nav>
        <div className="settings-body">
          <Embedded.Provider value={true}>
            {tab === "profiles" && <Profiles />}
            {tab === "users" && <UsersPage />}
            {tab === "tokens" && <Tokens />}
            {tab === "audit" && <Audit />}
          </Embedded.Provider>
          {tab === "editor" && <Config embedded />}
          {tab === "servers" && <Servers isAdmin={isAdmin} />}
          {TAB_KEYS[tab] && isAdmin && (
            <>
              <ErrorBox error={view.error ?? (Object.keys(errs).length ? null : error)} />
              {Object.keys(errs).length > 0 && (
                <div className="alert error" role="alert">
                  <Info size={16} />
                  <span>Corrija os campos marcados. Nada foi salvo.</span>
                </div>
              )}
              {draft && <SettingsForm tab={tab} s={draft} set={setDraft} errs={errs} />}
            </>
          )}
          {tab === "dvr" && <DVRStorages />}
          {TAB_KEYS[tab] && isAdmin && (
            <>
              {(dirty || saving) && (
                <div className="savebar">
                  <span>Alterações não salvas</span>
                  <div className="spacer" />
                  <button className="secondary" onClick={discard} disabled={saving}>
                    Descartar
                  </button>
                  <button onClick={() => void save()} disabled={saving}>
                    <Save size={15} /> {saving ? "Salvando…" : "Salvar e aplicar"}
                  </button>
                </div>
              )}
              {!dirty && savedAt > 0 && Date.now() - savedAt < 8000 && (
                <div className="savebar ok">
                  <Check size={16} />
                  <span>Salvo. Os servidores recebem as mudanças em poucos segundos.</span>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </>
  );
}

// ---------- fields ----------

function SettingsForm({
  tab,
  s,
  set,
  errs,
}: {
  tab: TabId;
  s: SettingsT;
  set: (s: SettingsT) => void;
  errs: FieldErrors;
}) {
  const profiles = useApi<Page<Profile>>("/transcode-profiles");
  const storages = useApi<Page<DVRStorage>>("/dvr/storages");
  const up = <K extends keyof SettingsT>(k: K, v: SettingsT[K]) => set({ ...s, [k]: v });
  const fo = s.default_failover;
  const upFo = <K extends keyof Failover>(k: K, v: Failover[K]) =>
    set({ ...s, default_failover: { ...fo, [k]: v } });
  const num = (
    k: keyof SettingsT,
    label: string,
    hint: ReactNode,
    o: { unit?: string; min?: number; max?: number; step?: number | "any" } = {},
  ) => (
    <Field label={label} hint={hint} err={errs[k]}>
      <NumInput value={s[k] as number} onChange={(n) => up(k, n as never)} {...o} />
    </Field>
  );
  const foNum = (
    k: keyof Failover,
    label: string,
    hint: string,
    o: { unit?: string; min?: number; max?: number; step?: number | "any" } = {},
  ) => (
    <Field label={label} hint={hint} err={errs["default_failover." + k]}>
      <NumInput value={fo[k] as number} onChange={(n) => upFo(k, n as never)} step="any" {...o} />
    </Field>
  );

  switch (tab) {
    case "general":
      return (
        <>
          <BrandingSection s={s} up={up} errs={errs} />
          <Section title="Geral">
            <Field
              label="Perfil padrão do modo automático"
              hint="Usado quando a fonte precisa ser transcodificada e o canal não escolheu perfil."
              err={errs.default_profile}
            >
              <select value={s.default_profile} onChange={(e) => up("default_profile", e.target.value)}>
                {!profiles.data?.items.some((p) => p.id === s.default_profile) && (
                  <option value={s.default_profile}>{s.default_profile}</option>
                )}
                {profiles.data?.items.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.id} · {p.video.width}×{p.video.height}
                  </option>
                ))}
              </select>
            </Field>
            <Field
              label="HEVC sem transcodificar"
              hint="Repassa H.265 direto. Nem todo player toca HLS com HEVC."
            >
              <span className="check-inline">
                <input
                  type="checkbox"
                  checked={s.allow_hevc_passthrough}
                  onChange={(e) => up("allow_hevc_passthrough", e.target.checked)}
                />
                {s.allow_hevc_passthrough ? "Permitido" : "Desligado (HEVC é convertido para H.264)"}
              </span>
            </Field>
          </Section>
        </>
      );
    case "ingest":
      return (
        <>
          <Section title="Recepção UDP">
            <Field
              label="Buffer de leitura UDP"
              hint="Por fonte. Aumente se aparecerem perdas com bitrates altos."
              err={errs.udp_read_buffer_bytes}
            >
              <NumInput
                value={Math.round((s.udp_read_buffer_bytes / 1048576) * 100) / 100}
                onChange={(n) => up("udp_read_buffer_bytes", Math.round(n * 1048576))}
                unit="MB"
                min={0.0625}
                max={256}
                step="any"
              />
            </Field>
          </Section>
          <Section title="Failover padrão dos canais novos">
            {foNum("no_packets_seconds", "Sem pacotes por", "Tempo sem receber nada até trocar de fonte.", {
              unit: "s",
            })}
            {foNum("confirm_window_seconds", "Confirmação", "Espera antes de confirmar a troca.", {
              unit: "s",
            })}
            {foNum("missing_stream_seconds", "Faixa ausente por", "Vídeo ou áudio sumido por esse tempo.", {
              unit: "s",
            })}
            <Field
              label="Bitrate baixo"
              hint="Abaixo dessa fração do bitrate normal conta como falha."
              err={errs["default_failover.low_bitrate_ratio"]}
            >
              <NumInput
                value={Math.round(fo.low_bitrate_ratio * 100)}
                onChange={(n) => upFo("low_bitrate_ratio", n / 100)}
                unit="%"
                min={1}
                max={99}
              />
            </Field>
            {foNum("low_bitrate_seconds", "Bitrate baixo por", "Tempo com bitrate baixo até trocar.", {
              unit: "s",
            })}
            <Field label="Voltar para a principal" hint="Quando a fonte principal se recupera.">
              <span className="check-inline">
                <input
                  type="checkbox"
                  checked={fo.return_to_primary}
                  onChange={(e) => upFo("return_to_primary", e.target.checked)}
                />
                {fo.return_to_primary ? "Sim" : "Não, fica no backup"}
              </span>
            </Field>
            {fo.return_to_primary &&
              foNum("primary_stable_seconds", "Principal estável por", "Antes de voltar para ela.", {
                unit: "s",
              })}
          </Section>
          <p className="small muted settings-note">
            Os padrões valem para canais criados a partir de agora. Canais existentes mantêm o que já têm.
          </p>
        </>
      );
    case "hls":
      return (
        <>
          <Section title="HLS ao vivo">
            {num("hls_segment_seconds", "Duração do segmento", "Menor = menos atraso, mais requisições.", {
              unit: "s",
              min: 1,
              max: 30,
            })}
            {num("hls_list_size", "Segmentos na playlist", "Janela que o player enxerga ao vivo.", {
              min: 3,
              max: 60,
            })}
            <Field label="Janela ao vivo" hint="Segmento × quantidade.">
              <div className="readout">{s.hls_segment_seconds * s.hls_list_size} s</div>
            </Field>
          </Section>
          <p className="small muted settings-note">
            Ao salvar, os canais reiniciam o empacotamento nos servidores (alguns segundos de interrupção).
          </p>
        </>
      );
    case "dvr":
      return (
        <>
          <Section title="Padrões de gravação">
            <Field
              label="Retenção padrão"
              hint="Para canais novos com gravação ligada."
              err={errs.dvr_default_retention_hours}
            >
              <NumInput
                value={Math.round((s.dvr_default_retention_hours / 24) * 100) / 100}
                onChange={(n) => up("dvr_default_retention_hours", Math.max(1, Math.round(n * 24)))}
                unit="dias"
                min={1}
                step="any"
              />
            </Field>
            <Field
              label="Armazenamento padrão"
              hint="Onde os canais novos gravam, se não escolherem outro."
              err={errs.dvr_default_storage}
            >
              <select
                value={s.dvr_default_storage}
                onChange={(e) => up("dvr_default_storage", e.target.value)}
              >
                <option value="">Pasta padrão de cada servidor</option>
                {storages.data?.items.map((st) => (
                  <option key={st.id} value={st.id}>
                    {st.id} · {st.mode === "single" ? "disco único" : st.mode.toUpperCase()} ·{" "}
                    {st.disks.length} disco(s)
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Arquivos" hint="Formato fixo.">
              <div className="readout">1 arquivo .ts por hora, por canal</div>
            </Field>
          </Section>
          <p className="small muted settings-note">
            A pasta padrão de cada servidor fica na aba Servidores. Armazenamentos com vários discos (RAID)
            logo abaixo.
          </p>
        </>
      );
    case "cluster":
      return (
        <>
          <Section title="Distribuição de canais">
            {num(
              "scheduler_max_cpu_percent",
              "CPU máxima por servidor",
              "Acima disso o servidor não recebe canais.",
              {
                unit: "%",
                min: 10,
                max: 100,
              },
            )}
            {num("cost_passthrough", "Custo de um passthrough", "Em % de CPU, estimado.", {
              unit: "%",
              step: "any",
            })}
            {num(
              "cost_transcode_per_megapixel",
              "Custo por megapixel transcodificado",
              "Em % de CPU, estimado.",
              {
                unit: "%",
                step: "any",
              },
            )}
            {num("cost_gpu_offload", "Redução com GPU", "O custo é dividido por esse valor com NVENC/QSV.", {
              unit: "×",
              step: "any",
            })}
          </Section>
          <Section title="Falha e manutenção">
            {num(
              "worker_offline_seconds",
              "Servidor offline após",
              "Sem heartbeat por esse tempo, os canais migram.",
              {
                unit: "s",
                min: 10,
                max: 600,
              },
            )}
            {num(
              "drain_migrations_per_pass",
              "Migrações por ciclo no drain",
              "Quantos canais saem por vez (a cada 5 s).",
              {
                min: 1,
                max: 100,
              },
            )}
          </Section>
          <p className="small muted settings-note">
            Os custos são estimativas provisórias até haver benchmark de laboratório.
          </p>
        </>
      );
    case "security":
      return (
        <Section title="Sessões do painel">
          {num(
            "access_token_minutes",
            "Validade do token de acesso",
            "Renovado sozinho enquanto o painel está aberto.",
            {
              unit: "min",
              min: 1,
            },
          )}
          {num("refresh_token_hours", "Sessão expira após", "Depois disso é preciso entrar de novo.", {
            unit: "h",
            min: 1,
          })}
          {num("login_attempts_per_minute", "Tentativas de login", "Por endereço IP, por minuto.", {
            min: 1,
          })}
        </Section>
      );
    default:
      return null;
  }
}

// ---------- servers ----------

function Servers({ isAdmin }: { isAdmin: boolean }) {
  const workers = useApi<Page<Worker>>("/workers", 5000);
  const list = useApi<{ items: ServerSettingsView[] }>(isAdmin ? "/settings/servers" : null, 15000);
  const error = workers.error ?? list.error;
  if (error) return <ErrorBox error={error} />;
  if (!workers.data || (isAdmin && !list.data)) return null;
  const byId = new Map(workers.data.items.map((w) => [w.id, w]));
  const views = new Map((list.data?.items ?? []).map((v) => [v.worker_id, v]));
  const ids = [...new Set([...byId.keys(), ...views.keys()])].sort();
  if (ids.length === 0) return <Empty icon={Server}>Nenhum servidor de mídia conectou ainda.</Empty>;
  return (
    <div className="stack">
      {isAdmin && (
        <p className="small muted settings-note" style={{ marginTop: 0 }}>
          Campos vazios mantêm o valor do arquivo .env de cada servidor.
        </p>
      )}
      {ids.map((id) => {
        const w = byId.get(id);
        const v = views.get(id);
        return (
          <section key={id} className="card settings-card">
            <div className="card-head">
              <h2>
                <Server size={16} /> {id}
              </h2>
              {w ? <WorkerChip state={w.state} /> : <span className="tag">nunca conectou</span>}
            </div>
            <div className={v ? "server-card" : ""}>
              {w ? (
                <WorkerSummary w={w} onChanged={() => void workers.reload()} />
              ) : (
                <div className="muted small">Sem heartbeat deste servidor.</div>
              )}
              {v && <ServerForm v={v} onSaved={() => void list.reload()} />}
            </div>
          </section>
        );
      })}
    </div>
  );
}

function ServerForm({ v, onSaved }: { v: ServerSettingsView; onSaved: () => void }) {
  const [d, setD] = useState<ServerSettings>(v.settings);
  const [ifaces, setIfaces] = useState(v.settings.ingest_interfaces.join(", "));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [ok, setOk] = useState(false);
  const errs = fieldErrors(error);
  // Only names the API accepts (Linux style; Windows names may have spaces).
  const detected = v.interfaces.filter((i) => /^[A-Za-z0-9._:@-]{1,32}$/.test(i));
  const parsed = ifaces
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);
  const dirty =
    JSON.stringify({ ...d, ingest_interfaces: parsed, roles: d.roles ?? [] }) !==
    JSON.stringify({ ...v.settings, roles: v.settings.roles ?? [] });
  const roles = d.roles ?? [];
  const toggleRole = (r: "passthrough" | "transcode" | "dvr") =>
    setD({ ...d, roles: roles.includes(r) ? roles.filter((x) => x !== r) : [...roles, r] });

  const save = async () => {
    setSaving(true);
    setError(null);
    setOk(false);
    try {
      await api.put(`/settings/servers/${encodeURIComponent(v.worker_id)}`, {
        ingest_interfaces: parsed,
        dvr_dir: d.dvr_dir,
        public_hls_base: d.public_hls_base,
        public_ts_base: d.public_ts_base,
        public_dvr_base: d.public_dvr_base,
        roles,
      });
      setOk(true);
      onSaved();
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };

  const text = (
    k: "dvr_dir" | "public_hls_base" | "public_ts_base" | "public_dvr_base",
    label: string,
    ph: string,
    hint: string,
  ) => (
    <Field label={label} hint={hint} err={errs[k]}>
      <input
        className="mono"
        value={d[k]}
        placeholder={ph}
        onChange={(e) => setD({ ...d, [k]: e.target.value })}
      />
    </Field>
  );

  return (
    <div className="server-form">
      {Object.keys(errs).length === 0 && <ErrorBox error={error} />}
      <div className="settings-grid">
        <Field
          label="Papéis do servidor"
          hint={
            roles.length
              ? "Só recebe canais compatíveis com os papéis marcados."
              : "Nenhum marcado: recebe qualquer canal."
          }
          err={Object.entries(errs).find(([k]) => k.startsWith("roles"))?.[1]}
        >
          <span className="row" style={{ gap: 6, flexWrap: "wrap" }}>
            {(
              [
                ["passthrough", "Ingestão/repasse"],
                ["transcode", "Transcodificação"],
                ["dvr", "DVR"],
              ] as const
            ).map(([r, label]) => (
              <button
                key={r}
                type="button"
                className={`tag tag-btn ${roles.includes(r) ? "accent" : ""}`}
                onClick={(e) => {
                  e.preventDefault();
                  toggleRole(r);
                }}
              >
                {roles.includes(r) ? "✓ " : ""}
                {label}
              </button>
            ))}
          </span>
        </Field>
        <Field
          label="Interfaces de ingestão"
          err={errs.ingest_interfaces}
          hint={
            detected.length ? (
              <span className="row" style={{ flexWrap: "wrap", gap: 4 }}>
                Detectadas:
                {detected.map((i) => (
                  <button
                    key={i}
                    type="button"
                    className="tag tag-btn"
                    onClick={(e) => {
                      e.preventDefault();
                      if (!parsed.includes(i)) setIfaces([...parsed, i].join(", "));
                    }}
                  >
                    {i}
                  </button>
                ))}
              </span>
            ) : (
              "Separe por vírgula. Vazio = todas."
            )
          }
        >
          <input
            className="mono"
            value={ifaces}
            placeholder="todas"
            onChange={(e) => setIfaces(e.target.value)}
          />
        </Field>
        {text("dvr_dir", "Pasta padrão do DVR", "do .env", "Usada pelos canais sem armazenamento próprio.")}
        {text(
          "public_hls_base",
          "URL pública do HLS",
          "https://origem.exemplo/hls",
          "Base dos links HLS mostrados no painel.",
        )}
        {text(
          "public_ts_base",
          "URL pública do MPEG-TS",
          "https://origem.exemplo/live",
          "Base dos links MPEG-TS por HTTP.",
        )}
        {text(
          "public_dvr_base",
          "URL pública do DVR",
          "https://origem.exemplo/dvr",
          "Base dos links de timeshift e catch-up.",
        )}
      </div>
      <div className="row" style={{ justifyContent: "flex-end", marginTop: 14 }}>
        {ok && !dirty && (
          <span className="small muted row" style={{ gap: 4 }}>
            <Check size={14} /> salvo
          </span>
        )}
        <button onClick={() => void save()} disabled={saving || !dirty}>
          <Save size={15} /> {saving ? "Salvando…" : "Salvar ajustes"}
        </button>
      </div>
    </div>
  );
}
