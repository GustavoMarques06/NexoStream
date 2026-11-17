import { AlertTriangle, CheckCircle2, Download, FileCode2, Play, RotateCcw, ShieldCheck } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api, getText } from "../api";
import { ErrorBox, PageHeader } from "../components/ui";
import { useCan } from "../me";
import type { ConfigResult } from "../types";

const KEYWORDS = new Set([
  "settings",
  "server",
  "name",
  "default_profile",
  "hevc_passthrough",
  "udp_buffer",
  "hls",
  "dvr_retention",
  "scheduler",
  "worker_offline",
  "drain_per_pass",
  "session",
  "login_limit",
  "interface",
  "dvr_dir",
  "public_hls",
  "public_ts",
  "public_dvr",
  "stream",
  "dvr_storage",
  "title",
  "group",
  "input",
  "mode",
  "transcode",
  "profile",
  "output",
  "dvr",
  "drm",
  "push",
  "auth",
  "epg",
  "failover",
  "disabled",
  "disk",
  "passthrough",
]);

/** highlight turns the config text into coloured spans (comments, strings, keywords, URLs, options). */
function highlight(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re =
    /(#[^\n]*)|("(?:[^"\\]|\\.)*"?)|([a-z]+:\/\/[^\s;{}#]+)|([A-Za-z_][\w-]*=[^\s;{}#]*)|([{};])|([A-Za-z_][\w-]*)|(\s+|[^\s])/g;
  let m: RegExpExecArray | null;
  let i = 0;
  let atStatementStart = true;
  while ((m = re.exec(text))) {
    const [tok, comment, str, url, opt, punct, word] = m;
    const k = i++;
    if (comment)
      out.push(
        <span key={k} className="hl-comment">
          {comment}
        </span>,
      );
    else if (str)
      out.push(
        <span key={k} className="hl-string">
          {str}
        </span>,
      );
    else if (url)
      out.push(
        <span key={k} className="hl-url">
          {url}
        </span>,
      );
    else if (opt) {
      const [key, ...rest] = opt.split("=");
      out.push(
        <span key={k}>
          <span className="hl-key">{key}</span>=<span className="hl-value">{rest.join("=")}</span>
        </span>,
      );
    } else if (punct)
      out.push(
        <span key={k} className="hl-punct">
          {punct}
        </span>,
      );
    else if (word && atStatementStart && KEYWORDS.has(word))
      out.push(
        <span key={k} className="hl-keyword">
          {word}
        </span>,
      );
    else out.push(tok);
    if (punct) atStatementStart = true;
    else if (!/^\s+$/.test(tok) && !comment) atStatementStart = false;
  }
  return out;
}

const HELP = `settings {                    # sistema (exige admin)
  name "NexoStream";  default_profile hd;  hevc_passthrough off;
  udp_buffer 4M;      failover no_packets=3 confirm=5 return=60;
  hls segment=6 window=10;   dvr_retention 3d;   dvr_storage none;
  scheduler max_cpu=80 passthrough=0.2 transcode_mp=4 gpu=4;
  worker_offline 15;  drain_per_pass 5;
  session access=15m refresh=12h;   login_limit 10;
}
server media-01 {                  # por servidor de mídia
  interface eno2;     dvr_dir /storage/dvr;
  public_hls https://origin/hls;  public_ts https://origin/live;  public_dvr https://origin/dvr;
}
stream <id> {
  title "Nome";               group Noticias;
  input udp://239.1.1.1:1234 interface=eno2;   # 1º = principal
  input srt://10.0.0.5:9000;                   # depois: backups
  input https://cdn/live/index.m3u8;           # rtmp://, rtsp://, http(s)://
  mode auto;            # auto | passthrough | transcode
  transcode hd;         # força transcodificação com o perfil
  profile hd;           # perfil usado só se precisar (auto)
  output hls; output dash; output mpegts_http; output udp://239.10.0.1:5000;
  push rtmp://a.rtmp.youtube.com/live2/CHAVE;   # ou srt://cdn:9000; um por linha
  dvr 3d storage=principal;    # ou: dvr /storage 72h;  dvr off;  (ABR: qualities=all grava todas)
  drm aes-128 rotate=1h;       # HLS e DVR criptografados, link com ?token=;  drm off;
  auth https://middleware/auth max_sessions=500 allow=10.0.0.0/8 deny=10.9.0.0/16 on_error=deny;  # auth off;
  epg https://epg.exemplo/guia.xml.gz id=canal.br;   # guia XMLTV;  epg off;
  overlay logo.png position=top-right width=200;     # logo no vídeo (só transcode);  overlay off;
  failover no_packets=3 confirm=5 return=60;   # return=off
  disabled;
}
dvr_storage principal {
  mode raid1;           # single | raid0 (distribui) | raid1 (espelha)
  disk /mnt/disk1;
  disk /mnt/disk2;
}`;

export function Config({ embedded = false }: { embedded?: boolean }) {
  const [text, setText] = useState("");
  const [saved, setSaved] = useState("");
  const [loadError, setLoadError] = useState<unknown>(null);
  const [result, setResult] = useState<ConfigResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [prune, setPrune] = useState(false);
  const [showHelp, setShowHelp] = useState(false);
  const area = useRef<HTMLTextAreaElement>(null);
  const hl = useRef<HTMLPreElement>(null);
  const gutter = useRef<HTMLDivElement>(null);
  const canWrite = useCan("channels:write");

  const load = useCallback(async () => {
    setLoadError(null);
    try {
      const t = await getText("/config");
      setText(t);
      setSaved(t);
      setResult(null);
    } catch (e) {
      setLoadError(e);
    }
  }, []);
  useEffect(() => void load(), [load]);

  const validate = useCallback(async () => {
    setBusy(true);
    try {
      setResult(await api.post<ConfigResult>("/config/validate", { text, prune }));
    } catch (e) {
      setLoadError(e);
    } finally {
      setBusy(false);
    }
  }, [text, prune]);

  const apply = async () => {
    setBusy(true);
    try {
      const res = await api.post<ConfigResult>("/config/apply", { text, prune }).catch(async (e: unknown) => {
        // 422 carries the validation result
        const err = e as { status?: number };
        if (err.status === 422) return api.post<ConfigResult>("/config/validate", { text, prune });
        throw e;
      });
      setResult(res);
      if (res.applied) {
        const t = await getText("/config");
        setText(t);
        setSaved(t);
      }
    } catch (e) {
      setLoadError(e);
    } finally {
      setBusy(false);
    }
  };

  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "nexostream.conf";
    a.click();
    URL.revokeObjectURL(url);
  };

  const goToLine = (line: number) => {
    const ta = area.current;
    if (!ta || line < 1) return;
    const lines = text.split("\n");
    const start = lines.slice(0, line - 1).reduce((n, l) => n + l.length + 1, 0);
    ta.focus();
    ta.setSelectionRange(start, start + (lines[line - 1]?.length ?? 0));
    ta.scrollTop = Math.max(0, (line - 4) * 20);
  };

  const syncScroll = () => {
    const ta = area.current;
    if (!ta) return;
    if (hl.current) {
      hl.current.scrollTop = ta.scrollTop;
      hl.current.scrollLeft = ta.scrollLeft;
    }
    if (gutter.current) gutter.current.scrollTop = ta.scrollTop;
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      void validate();
    } else if (e.key === "Tab") {
      e.preventDefault();
      const ta = e.currentTarget;
      const { selectionStart: a, selectionEnd: b } = ta;
      const next = text.slice(0, a) + "  " + text.slice(b);
      setText(next);
      requestAnimationFrame(() => ta.setSelectionRange(a + 2, a + 2));
    }
  };

  const lineCount = text.split("\n").length;
  const errorLines = useMemo(() => new Set(result?.errors.map((e) => e.line) ?? []), [result]);
  const dirty = text !== saved;
  const p = result;
  const changes = p
    ? (p.settings === "update" ? 1 : 0) +
      p.servers.create.length +
      p.servers.update.length +
      p.channels.create.length +
      p.channels.update.length +
      p.channels.delete.length +
      p.storages.create.length +
      p.storages.update.length +
      p.storages.delete.length
    : 0;

  const bar = (children: ReactNode) =>
    embedded ? (
      <div className="toolbar">
        <span className="muted small">Sistema, servidores, armazenamentos e canais em texto</span>
        <div className="spacer" />
        {children}
      </div>
    ) : (
      <PageHeader
        title="Configuração"
        sub="Sistema, servidores, armazenamentos e canais em texto, como no Flussonic"
      >
        {children}
      </PageHeader>
    );

  return (
    <>
      {bar(
        <>
          <button className="secondary" onClick={() => setShowHelp((s) => !s)}>
            <FileCode2 size={15} /> Sintaxe
          </button>
          <button className="secondary" onClick={download} title="Baixar nexostream.conf">
            <Download size={15} />
          </button>
          <button
            className="secondary"
            onClick={() => void load()}
            disabled={busy}
            title="Descartar edições e recarregar"
          >
            <RotateCcw size={15} />
          </button>
          <button className="secondary" onClick={() => void validate()} disabled={busy}>
            <ShieldCheck size={15} /> Validar
          </button>
          {canWrite && (
            <button onClick={() => void apply()} disabled={busy || (!dirty && !prune)}>
              <Play size={15} /> Aplicar
            </button>
          )}
        </>,
      )}
      <ErrorBox error={loadError} />
      {showHelp && <pre className="card config-help">{HELP}</pre>}

      <div className="config-layout">
        <div className={`editor ${dirty ? "dirty" : ""}`}>
          <div className="editor-gutter" ref={gutter} aria-hidden>
            {Array.from({ length: lineCount }, (_, i) => (
              <div key={i} className={errorLines.has(i + 1) ? "err" : ""}>
                {i + 1}
              </div>
            ))}
          </div>
          <div className="editor-code">
            <pre ref={hl} className="editor-hl" aria-hidden>
              {highlight(text)}
              {"\n"}
            </pre>
            <textarea
              ref={area}
              value={text}
              spellCheck={false}
              onChange={(e) => {
                setText(e.target.value);
                setResult(null);
              }}
              onScroll={syncScroll}
              onKeyDown={onKeyDown}
              aria-label="Configuração"
            />
          </div>
        </div>

        <aside className="stack">
          <section className="card">
            <div className="card-head">
              <h2>Resultado</h2>
              <div className="spacer" />
              {dirty && <span className="tag accent">editado</span>}
            </div>
            {!p && <div className="muted small">Valide (Ctrl+S) para ver erros e o que vai mudar.</div>}
            {p && p.errors.length > 0 && (
              <div className="list">
                {p.errors.map((e, i) => (
                  <button key={i} className="config-err" onClick={() => goToLine(e.line)}>
                    <AlertTriangle size={14} />
                    <span>
                      {e.line > 0 && <strong>linha {e.line}: </strong>}
                      {e.message}
                    </span>
                  </button>
                ))}
              </div>
            )}
            {p && p.ok && (
              <div className={`alert ${p.applied ? "info" : "ok-box"}`}>
                <CheckCircle2 size={16} />
                <span>
                  {p.applied
                    ? "Configuração aplicada."
                    : changes
                      ? `Válida: ${changes} alteração(ões).`
                      : "Válida, sem alterações."}
                </span>
              </div>
            )}
            {p && p.ok && (
              <div className="plan">
                {p.settings && (
                  <div className="plan-row">
                    <div className="small muted">
                      Sistema · {p.settings === "update" ? "será alterado" : "sem mudança"}
                    </div>
                  </div>
                )}
                {p.servers.create.length + p.servers.update.length + p.servers.unchanged > 0 && (
                  <PlanRow label="Servidores" plan={p.servers} />
                )}
                <PlanRow label="Canais" plan={p.channels} />
                <PlanRow label="Armazenamentos" plan={p.storages} />
              </div>
            )}
          </section>
          <label className="check card">
            <input type="checkbox" checked={prune} onChange={(e) => setPrune(e.target.checked)} />
            <span>
              Remover canais e armazenamentos que não estão no texto
              <div className="small muted">Sem isso, o texto só cria e altera.</div>
            </span>
          </label>
        </aside>
      </div>
    </>
  );
}

function PlanRow({ label, plan }: { label: string; plan: ConfigResult["channels"] }) {
  const items: [string, string[], string][] = [
    ["criar", plan.create, "s-running"],
    ["alterar", plan.update, "s-on_backup"],
    ["remover", plan.delete, "s-failed"],
  ];
  return (
    <div className="plan-row">
      <div className="small muted">
        {label} · {plan.unchanged} sem mudança
      </div>
      {items.map(([verb, ids, cls]) =>
        ids.length ? (
          <div key={verb} className="row" style={{ flexWrap: "wrap", gap: 4, marginTop: 6 }}>
            <span className={`chip ${cls}`}>
              <span className="dot" />
              {verb} {ids.length}
            </span>
            {ids.map((id) => (
              <span key={id} className="tag">
                {id}
              </span>
            ))}
          </div>
        ) : null,
      )}
    </div>
  );
}
