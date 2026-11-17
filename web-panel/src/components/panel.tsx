import { Check, RotateCcw, Save, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

/** Building blocks of the channel tabs: panels, setting rows and switches. */

export function Panel({
  icon: Icon,
  title,
  sub,
  actions,
  flush,
  children,
}: {
  icon?: LucideIcon;
  title: string;
  sub?: ReactNode;
  actions?: ReactNode;
  flush?: boolean;
  children: ReactNode;
}) {
  return (
    <section className="panel">
      <header className="panel-head">
        {Icon && (
          <span className="panel-icon">
            <Icon size={16} />
          </span>
        )}
        <div style={{ minWidth: 0 }}>
          <h2>{title}</h2>
          {sub && <div className="panel-sub">{sub}</div>}
        </div>
        <div className="spacer" />
        {actions}
      </header>
      <div className={flush ? "panel-body flush" : "panel-body"}>{children}</div>
    </section>
  );
}

/** Row is one setting: title and explanation on the left, control on the right. */
export function Row({
  icon: Icon,
  title,
  hint,
  err,
  stack,
  children,
}: {
  icon?: LucideIcon;
  title: ReactNode;
  hint?: ReactNode;
  err?: string;
  /** stack puts the control under the text (long inputs, text areas). */
  stack?: boolean;
  children?: ReactNode;
}) {
  return (
    <div className={`prow ${Icon ? "" : "noicon"} ${stack ? "stack" : ""} ${err ? "invalid" : ""}`}>
      {Icon && (
        <span className="prow-icon">
          <Icon size={16} />
        </span>
      )}
      <div className="prow-text">
        <div className="prow-title">{title}</div>
        {hint && <div className="prow-hint">{hint}</div>}
        {err && <div className="prow-err">{err}</div>}
      </div>
      {children !== undefined && <div className="prow-control">{children}</div>}
    </div>
  );
}

export function Switch({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  label: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      title={label}
      className={`switch ${checked ? "on" : ""}`}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    >
      <span />
    </button>
  );
}

/** Feature is the empty state of a function that is off or not set up. */
export function Feature({
  icon: Icon,
  title,
  children,
  action,
}: {
  icon: LucideIcon;
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="feature">
      <span className="feature-icon">
        <Icon size={24} />
      </span>
      <h3>{title}</h3>
      <div className="feature-text">{children}</div>
      {action && <div className="feature-action">{action}</div>}
    </div>
  );
}

/** KV is a vertical list of facts (side panels). */
export function KV({ items }: { items: [ReactNode, ReactNode, string?][] }) {
  return (
    <dl className="kv">
      {items.map(([k, v, cls], i) => (
        <div key={i}>
          <dt>{k}</dt>
          <dd className={cls}>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

/** SaveBar appears only while there are unsaved changes (and briefly after saving). */
export function SaveBar({
  dirty,
  saving,
  ok,
  onSave,
  onReset,
}: {
  dirty: boolean;
  saving: boolean;
  ok: boolean;
  onSave: () => void;
  onReset: () => void;
}) {
  if (!dirty && !saving && !ok) return null;
  if (!dirty && ok)
    return (
      <div className="savebar ok">
        <Check size={16} /> Alterações salvas
      </div>
    );
  return (
    <div className="savebar">
      <span className="savebar-dot" /> Alterações não salvas
      <div className="spacer" />
      <button className="ghost" onClick={onReset} disabled={saving}>
        <RotateCcw size={14} /> Descartar
      </button>
      <button onClick={onSave} disabled={saving}>
        <Save size={15} /> {saving ? "Salvando…" : "Salvar alterações"}
      </button>
    </div>
  );
}
