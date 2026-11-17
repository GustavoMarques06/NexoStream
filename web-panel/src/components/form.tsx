import type { ReactNode } from "react";
import { ApiError } from "../api";

/** Form building blocks shared by the Settings page and the channel tabs. */

export type FieldErrors = Record<string, string>;

export function fieldErrors(e: unknown): FieldErrors {
  if (e instanceof ApiError && e.details && typeof e.details === "object") return e.details as FieldErrors;
  return {};
}

export function Field({
  label,
  hint,
  err,
  children,
}: {
  label: string;
  hint?: ReactNode;
  err?: string;
  children: ReactNode;
}) {
  return (
    <label className={`setting ${err ? "invalid" : ""}`}>
      <span className="setting-label">{label}</span>
      {children}
      {err ? (
        <span className="setting-err">{err}</span>
      ) : (
        hint && <span className="setting-hint">{hint}</span>
      )}
    </label>
  );
}

export function NumInput({
  value,
  onChange,
  unit,
  min,
  max,
  step = 1,
}: {
  value: number;
  onChange: (n: number) => void;
  unit?: string;
  min?: number;
  max?: number;
  step?: number | "any";
}) {
  return (
    <div className="num-unit">
      <input
        type="number"
        value={Number.isFinite(value) ? value : ""}
        min={min}
        max={max}
        step={step}
        onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))}
      />
      {unit && <span className="unit">{unit}</span>}
    </div>
  );
}

export function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="card settings-card">
      <h2>{title}</h2>
      <div className="settings-grid">{children}</div>
    </section>
  );
}
