import { AlertOctagon, AlertTriangle, BellRing, CheckCircle2, Server, Tv } from "lucide-react";
import { Link } from "react-router-dom";
import { Panel } from "../components/panel";
import { Empty, ErrorBox, PageHeader } from "../components/ui";
import { ago } from "../format";
import { useApi } from "../hooks";

export interface AlertItem {
  id: string;
  rule: string;
  severity: "critical" | "warning";
  title: string;
  detail: string;
  channel_id?: string;
  worker_id?: string;
  since: string;
}

export interface AlertsResponse {
  items: AlertItem[];
  counts: { critical: number; warning: number };
  evaluated_at: string;
}

const RULE: Record<string, string> = {
  no_signal: "Sem sinal",
  on_backup: "Fonte alternativa",
  restarts: "Reinícios",
  cc_errors: "Erros CC",
  worker_offline: "Servidor",
  dvr_disk: "Disco do DVR",
  dvr_integrity: "Gravação",
  node_resources: "Recursos",
};

/** Alerts lists the active problems (spec 14.3), refreshed every 10 s. */
export function Alerts() {
  const data = useApi<AlertsResponse>("/alerts", 10000);
  const items = data.data?.items ?? [];
  const crit = items.filter((a) => a.severity === "critical");
  const warn = items.filter((a) => a.severity === "warning");
  const list = (xs: AlertItem[]) =>
    xs.map((a) => (
      <div key={a.id} className={`alert-row ${a.severity}`}>
        <span className="alert-icon">
          {a.severity === "critical" ? <AlertOctagon size={17} /> : <AlertTriangle size={17} />}
        </span>
        <div className="alert-body">
          <div className="alert-title">
            {a.title}
            <span className="tag">{RULE[a.rule] ?? a.rule}</span>
          </div>
          <div className="alert-detail">{a.detail}</div>
        </div>
        <span className="alert-since" title={new Date(a.since).toLocaleString("pt-BR")}>
          {ago(a.since)}
        </span>
        {a.channel_id ? (
          <Link className="btn-link" to={`/channels/${a.channel_id}`}>
            <Tv size={14} /> Abrir canal
          </Link>
        ) : a.worker_id ? (
          <Link className="btn-link" to="/settings?tab=servers">
            <Server size={14} /> Servidores
          </Link>
        ) : null}
      </div>
    ));
  return (
    <>
      <PageHeader
        title="Alertas"
        sub={data.data ? `avaliado ${ago(data.data.evaluated_at)} · atualiza a cada 10 s` : undefined}
      />
      <ErrorBox error={data.error} />
      {data.data && !items.length && (
        <Panel icon={CheckCircle2} title="Tudo certo">
          <Empty icon={CheckCircle2} ok>
            Nenhum alerta ativo: canais com sinal, servidores no ar e discos com espaço.
          </Empty>
        </Panel>
      )}
      {!!crit.length && (
        <Panel icon={AlertOctagon} title={`Críticos · ${crit.length}`} sub="Afetam o ar agora." flush>
          {list(crit)}
        </Panel>
      )}
      {!!warn.length && (
        <Panel
          icon={BellRing}
          title={`Avisos · ${warn.length}`}
          sub="Precisam de atenção, mas o canal segue no ar."
          flush
        >
          {list(warn)}
        </Panel>
      )}
      <p className="small muted">
        Regras: sem pacotes há 10 s; na fonte alternativa há 10 min; mais de 5 reinícios em 10 min; mais de
        100 erros CC por minuto; servidor sem heartbeat; disco do DVR acima de 85%; CPU ou GPU acima de 90%.
        As mesmas regras existem no Prometheus para notificação por e-mail ou chat.
      </p>
    </>
  );
}
