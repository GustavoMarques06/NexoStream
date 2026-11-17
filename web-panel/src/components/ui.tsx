import { createContext, useContext, useEffect, useId, type ReactNode } from "react";
import {
  AlertTriangle,
  ArrowLeftRight,
  CircleCheck,
  CircleDot,
  Inbox,
  RotateCcw,
  Server,
  ServerOff,
  Shuffle,
  X,
  type LucideIcon,
} from "lucide-react";
import { ApiError } from "../api";
import { STATE_LABEL } from "../format";
import type { ChannelState, NsEvent } from "../types";

export const stateClass = (s: string) => `s-${s.toLowerCase()}`;

export function StateChip({ state }: { state: ChannelState }) {
  return (
    <span className={`chip ${stateClass(state)}`}>
      <span className="dot" />
      {STATE_LABEL[state] ?? state}
    </span>
  );
}

const WORKER_LABEL: Record<string, string> = { ONLINE: "Online", DRAINING: "Em drain", OFFLINE: "Offline" };

export function WorkerChip({ state }: { state: string }) {
  return (
    <span className={`chip w-${state.toLowerCase()}`}>
      <span className="dot" />
      {WORKER_LABEL[state] ?? state}
    </span>
  );
}

/** Embedded is true inside Settings tabs: page headers turn into toolbars. */
export const Embedded = createContext(false);

export function PageHeader({
  title,
  sub,
  children,
}: {
  title: ReactNode;
  sub?: ReactNode;
  children?: ReactNode;
}) {
  const embedded = useContext(Embedded);
  if (embedded)
    return sub || children ? (
      <div className="toolbar">
        {sub && <span className="muted small">{sub}</span>}
        <div className="spacer" />
        {children}
      </div>
    ) : null;
  return (
    <header className="page-head">
      <div>
        <h1>{title}</h1>
        {sub && <div className="sub">{sub}</div>}
      </div>
      <div className="spacer" />
      {children}
    </header>
  );
}

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  const e = error as ApiError;
  const details =
    e.details && typeof e.details === "object" ? Object.entries(e.details as Record<string, unknown>) : [];
  return (
    <div className="alert error" role="alert">
      <AlertTriangle size={16} />
      <div>
        <strong>{e.message ?? String(error)}</strong>
        {details.length > 0 && (
          <ul>
            {details.map(([k, v]) => (
              <li key={k}>
                <code>{k}</code>: {String(v)}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

export function Modal({
  title,
  onClose,
  children,
  wide,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
}) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [onClose]);
  return (
    <div className="modal-backdrop" onMouseDown={onClose}>
      <div
        className={`modal ${wide ? "wide" : ""}`}
        role="dialog"
        aria-label={title}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header>
          <h2>{title}</h2>
          <button className="icon" onClick={onClose} aria-label="Fechar">
            <X size={18} />
          </button>
        </header>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}

export function Kpi({
  icon: Icon,
  label,
  value,
  tone,
  extra,
}: {
  icon: LucideIcon;
  label: string;
  value: ReactNode;
  tone?: "ok" | "bad" | "warn";
  extra?: ReactNode;
}) {
  return (
    <div className="card kpi">
      <div className={`kpi-icon ${tone ?? ""}`}>
        <Icon size={19} />
      </div>
      <div style={{ minWidth: 0, flex: 1 }}>
        <div className="kpi-label">{label}</div>
        <div className="kpi-value">{value}</div>
        {extra}
      </div>
    </div>
  );
}

export function Empty({
  icon: Icon = Inbox,
  children,
  ok,
}: {
  icon?: LucideIcon;
  children: ReactNode;
  ok?: boolean;
}) {
  return (
    <div className={`empty ${ok ? "ok" : ""}`}>
      <Icon size={30} strokeWidth={1.5} />
      <div>{children}</div>
    </div>
  );
}

export function Meter({ label, percent }: { label: string; percent: number | null | undefined }) {
  const has = percent !== null && percent !== undefined;
  const p = has ? Math.max(0, Math.min(100, percent)) : 0;
  const tone = p >= 90 ? "bad" : p >= 75 ? "warn" : "";
  return (
    <div className="meter">
      <span className="meter-label">{label}</span>
      <div className="meter-track">
        {has && <div className={`meter-fill ${tone}`} style={{ width: `${p}%` }} />}
      </div>
      <span className="meter-value">{has ? `${p.toFixed(0)}%` : "—"}</span>
    </div>
  );
}

/** Sparkline draws a small area chart of recent values. */
export function Sparkline({
  values,
  width = 120,
  height = 34,
  color = "var(--accent)",
}: {
  values: number[];
  width?: number;
  height?: number;
  color?: string;
}) {
  const id = "sg" + useId().replace(/:/g, "");
  if (values.length < 2) return <svg className="sparkline" width={width} height={height} />;
  const max = Math.max(...values, 1);
  const step = width / (values.length - 1);
  const pts = values.map((v, i) => [i * step, height - 2 - (v / max) * (height - 4)] as const);
  const line = pts.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
  return (
    <svg className="sparkline" width={width} height={height} viewBox={`0 0 ${width} ${height}`}>
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.35" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={`${line} L${width},${height} L0,${height} Z`} fill={`url(#${id})`} />
      <path d={line} fill="none" stroke={color} strokeWidth="1.6" strokeLinejoin="round" />
    </svg>
  );
}

/** eventLook returns the icon and colour class of an event. */
export function eventLook(e: NsEvent): { icon: LucideIcon; cls: string } {
  switch (e.type) {
    case "channel.state_changed":
      return { icon: CircleDot, cls: stateClass(e.to ?? "stopped") };
    case "channel.source_switched":
      return { icon: ArrowLeftRight, cls: e.reason === "primary_recovered" ? "s-running" : "s-on_backup" };
    case "channel.pipeline_restarted":
      return { icon: RotateCcw, cls: "s-failed" };
    case "channel.input_changed":
      return { icon: Shuffle, cls: "s-starting" };
    case "worker.online":
      return { icon: Server, cls: "s-running" };
    case "worker.offline":
      return { icon: ServerOff, cls: "s-failed" };
    case "worker.channels_reassigned":
      return { icon: Shuffle, cls: "s-on_backup" };
    default:
      return { icon: CircleCheck, cls: "s-stopped" };
  }
}
