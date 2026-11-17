import { useState } from "react";
import { api } from "../api";
import { ErrorBox, Meter } from "../components/ui";
import { ago, bitrate } from "../format";
import { useCan } from "../me";
import type { Worker } from "../types";

/** WorkerSummary shows a Media Node's health, load and drain control (Settings › Servidores). */
export function WorkerSummary({ w, onChanged }: { w: Worker; onChanged: () => void }) {
  const [error, setError] = useState<unknown>(null);
  const canWrite = useCan("workers:write");

  const drain = async (on: boolean) => {
    if (on && !confirm(`Colocar ${w.id} em drain? Os canais serão migrados.`)) return;
    setError(null);
    try {
      await api.post(`/workers/${w.id}/${on ? "drain" : "undrain"}`);
      onChanged();
    } catch (e) {
      setError(e);
    }
  };

  return (
    <div className="worker-summary">
      <ErrorBox error={error} />
      <div className="small muted">
        v{w.version} · heartbeat {ago(w.last_heartbeat_at)}
      </div>
      <div className="worker-meters">
        <Meter label="CPU" percent={w.resources.cpu_percent} />
        <Meter
          label="RAM"
          percent={
            w.resources.memory_total_bytes
              ? (w.resources.memory_used_bytes / w.resources.memory_total_bytes) * 100
              : null
          }
        />
        <Meter label="GPU" percent={w.resources.gpu_percent} />
      </div>
      <div className="worker-stats" style={{ marginBottom: 12 }}>
        <span>
          <strong>{w.channels}</strong> canais
        </span>
        <span>↓ {bitrate(w.resources.network_in_bps)}</span>
        <span>↑ {bitrate(w.resources.network_out_bps)}</span>
      </div>
      <div className="row" style={{ flexWrap: "wrap" }}>
        {(w.capabilities.encoders ?? []).map((e) => (
          <span key={e} className="tag">
            {e.toUpperCase()}
          </span>
        ))}
        <div className="spacer" />
        {canWrite && w.state !== "OFFLINE" && (
          <button className="secondary sm" onClick={() => void drain(!w.draining)}>
            {w.draining ? "Retirar drain" : "Drain"}
          </button>
        )}
      </div>
    </div>
  );
}
