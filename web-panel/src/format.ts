import type { ChannelState } from "./types";

export function bitrate(bps?: number | null): string {
  if (bps === undefined || bps === null) return "—";
  if (bps >= 1e9) return (bps / 1e9).toFixed(2) + " Gbps";
  if (bps >= 1e6) return (bps / 1e6).toFixed(2) + " Mbps";
  if (bps >= 1e3) return (bps / 1e3).toFixed(0) + " kbps";
  return bps.toFixed(0) + " bps";
}

/** bitrateParts splits a bitrate into value and unit, e.g. ["2.49", "Mbps"]. */
export function bitrateParts(bps?: number | null): [string, string] {
  if (!bps) return ["—", ""];
  const [v, u] = bitrate(bps).split(" ");
  return [v ?? "—", u ?? ""];
}

export function bytes(n?: number | null): string {
  if (!n) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return v.toFixed(i === 0 ? 0 : 1) + " " + u[i];
}

export function dateTime(s?: string | null): string {
  if (!s || s.startsWith("0001-")) return "—";
  return new Date(s).toLocaleString("pt-BR");
}

export function ago(s?: string | null): string {
  if (!s || s.startsWith("0001-")) return "—";
  const sec = Math.max(0, Math.round((Date.now() - new Date(s).getTime()) / 1000));
  if (sec < 60) return `há ${sec} s`;
  if (sec < 3600) return `há ${Math.floor(sec / 60)} min`;
  if (sec < 86400) return `há ${Math.floor(sec / 3600)} h`;
  return `há ${Math.floor(sec / 86400)} d`;
}

export const STATE_LABEL: Record<ChannelState, string> = {
  STOPPED: "Parado",
  STARTING: "Iniciando",
  RUNNING: "No ar",
  DEGRADED: "Degradado",
  SWITCHING: "Trocando fonte",
  ON_BACKUP: "Em backup",
  RECOVERING: "Recuperando",
  FAILED: "Falha",
};

export const EVENT_LABEL: Record<string, string> = {
  "channel.state_changed": "Mudança de estado",
  "channel.source_switched": "Troca de fonte",
  "channel.pipeline_restarted": "Reinício do pipeline",
  "channel.input_changed": "Mudança na entrada",
  "worker.online": "Worker online",
  "worker.offline": "Worker offline",
  "worker.channels_reassigned": "Canais realocados",
  "dvr.storage_warning": "Alerta de disco do DVR",
  "dvr.integrity_error": "Problema na gravação",
  "worker.encoder_failed": "Encoder de hardware falhou",
};

export const REASON_LABEL: Record<string, string> = {
  start: "início",
  stop: "parado",
  started: "iniciado",
  no_packets: "sem pacotes",
  low_bitrate: "bitrate baixo",
  no_video: "sem vídeo",
  no_audio: "sem áudio",
  no_pmt: "sem PAT/PMT",
  failover: "failover",
  manual: "manual",
  manual_restart: "reinício manual",
  recovered: "recuperado",
  primary_seen: "principal voltou",
  primary_unstable: "principal instável",
  primary_recovered: "retorno à principal",
  pipeline_exited: "pipeline encerrado",
  exited: "processo encerrado",
  start_failed: "falha ao iniciar o pipeline",
  pipeline_stalled: "pipeline travado",
  restart_limit: "limite de reinícios",
  no_source_available: "nenhuma fonte disponível",
  source_available: "fonte disponível",
  cooldown_elapsed: "nova tentativa",
  awaiting_worker: "aguardando worker",
  missed_heartbeats: "heartbeats perdidos",
  worker_offline: "worker offline",
  drain: "drain",
  stopping: "parando",
};

export function reason(r?: string): string {
  if (!r) return "";
  return REASON_LABEL[r] ?? r;
}

export function describeEvent(e: {
  type: string;
  from?: string;
  to?: string;
  reason?: string;
  details?: Record<string, unknown>;
}): string {
  switch (e.type) {
    case "channel.state_changed":
      return `${STATE_LABEL[e.from as ChannelState] ?? e.from} → ${STATE_LABEL[e.to as ChannelState] ?? e.to}${e.reason ? ` (${reason(e.reason)})` : ""}`;
    case "channel.source_switched":
      return `fonte ${String(e.details?.from_priority ?? "?")} → ${String(e.details?.to_priority ?? "?")} (${reason(e.reason)})`;
    case "channel.pipeline_restarted":
      return `${reason(e.reason)}${e.details?.delay_seconds ? `, nova tentativa em ${String(e.details.delay_seconds)} s` : ""}`;
    case "worker.channels_reassigned":
      return `${String(e.details?.count ?? "")} canal(is) — ${reason(e.reason)}`;
    default:
      return e.reason ? reason(e.reason) : "";
  }
}
