import { Archive, HardDrive, Pencil, Plus, Server, Trash2, X } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api } from "../api";
import { Empty, ErrorBox, Meter, Modal, StateChip } from "../components/ui";
import { bytes } from "../format";
import { useApi } from "../hooks";
import { useCan } from "../me";
import type { Channel, DVRStorage, Page, Worker } from "../types";

const MODES: { id: DVRStorage["mode"]; title: string; hint: string }[] = [
  { id: "single", title: "Disco único", hint: "Grava em uma pasta" },
  {
    id: "raid0",
    title: "RAID-0 · distribuído",
    hint: "Espalha os segmentos: soma capacidade e velocidade, sem redundância",
  },
  {
    id: "raid1",
    title: "RAID-1 · espelhado",
    hint: "Cada segmento em todos os discos: se um falhar, a gravação continua",
  },
];

const MODE_LABEL: Record<string, string> = { single: "Único", raid0: "RAID-0", raid1: "RAID-1" };

function retention(h: number) {
  return h % 24 === 0 ? `${h / 24} d` : `${h} h`;
}

/** DVRStorages manages storages (disks and RAID) and shows disks and recording channels (Settings › DVR). */
export function DVRStorages() {
  const storages = useApi<Page<DVRStorage>>("/dvr/storages");
  const workers = useApi<Page<Worker>>("/workers", 10000);
  const channels = useApi<Page<Channel>>("/channels?limit=200", 15000);
  const [editing, setEditing] = useState<DVRStorage | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const canWrite = useCan("dvr:write");

  const recording = (channels.data?.items ?? []).filter((c) => c.dvr.enabled);
  const usage = (id: string) => recording.filter((c) => c.dvr.storage === id).length;

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!editing) return;
    setError(null);
    try {
      const body = { ...editing, disks: editing.disks.map((d) => d.trim()).filter(Boolean) };
      if (isNew) await api.post("/dvr/storages", body);
      else await api.patch(`/dvr/storages/${editing.id}`, { mode: body.mode, disks: body.disks });
      setEditing(null);
      void storages.reload();
    } catch (err) {
      setError(err);
    }
  };

  const remove = async (s: DVRStorage) => {
    if (!confirm(`Remover o armazenamento "${s.id}"? Os arquivos gravados não são apagados.`)) return;
    try {
      await api.del(`/dvr/storages/${s.id}`);
      void storages.reload();
    } catch (err) {
      setError(err);
    }
  };

  return (
    <>
      <ErrorBox error={storages.error ?? (!editing ? error : null)} />

      <section className="card" style={{ marginBottom: 16 }}>
        <div className="card-head">
          <h2>
            <Archive size={16} /> Armazenamentos e RAID
          </h2>
          <div className="spacer" />
          {canWrite && (
            <button
              className="sm"
              onClick={() => {
                setIsNew(true);
                setEditing({ id: "", mode: "raid1", disks: ["", ""] });
              }}
            >
              <Plus size={15} /> Novo armazenamento
            </button>
          )}
        </div>
        {storages.data?.items.length === 0 ? (
          <Empty icon={HardDrive}>
            Nenhum armazenamento. Os canais gravam na pasta padrão de cada servidor (aba Servidores).
          </Empty>
        ) : (
          <div className="worker-grid">
            {storages.data?.items.map((s) => (
              <div key={s.id} className="storage-card">
                <div className="row">
                  <strong>{s.id}</strong>
                  <span className={`tag ${s.mode === "raid1" ? "accent" : ""}`}>
                    {MODE_LABEL[s.mode] ?? s.mode}
                  </span>
                  <div className="spacer" />
                  {canWrite && (
                    <>
                      <button
                        className="icon"
                        title="Editar"
                        onClick={() => {
                          setIsNew(false);
                          setEditing(structuredClone(s));
                        }}
                      >
                        <Pencil size={15} />
                      </button>
                      <button className="icon" title="Remover" onClick={() => void remove(s)}>
                        <Trash2 size={15} />
                      </button>
                    </>
                  )}
                </div>
                <div className="disks">
                  {s.disks.map((d) => (
                    <div key={d} className="disk">
                      <HardDrive size={14} /> <span className="mono">{d}</span>
                    </div>
                  ))}
                </div>
                <div className="small muted">{usage(s.id)} canal(is) gravando aqui</div>
              </div>
            ))}
          </div>
        )}
      </section>

      <section style={{ marginBottom: 16 }}>
        <div className="card-head">
          <h2>
            <Server size={16} /> Discos dos servidores
          </h2>
        </div>
        <div className="worker-grid">
          {workers.data?.items.map((w) => (
            <div key={w.id} className="card">
              <div className="row" style={{ marginBottom: 12 }}>
                <strong>{w.id}</strong>
                <div className="spacer" />
                <span className="small muted">
                  {bytes(w.resources.dvr_used_bytes)} / {bytes(w.resources.dvr_total_bytes)}
                </span>
              </div>
              {(w.resources.dvr_disks ?? []).length === 0 && (
                <div className="muted small">Sem informação de discos</div>
              )}
              <div className="worker-meters" style={{ margin: 0 }}>
                {(w.resources.dvr_disks ?? []).map((d) => (
                  <div key={d.path}>
                    <div className="row small" style={{ marginBottom: 4 }}>
                      <span className="mono truncate" title={d.path}>
                        {d.path}
                      </span>
                      <div className="spacer" />
                      <span className={`chip ${d.down ? "s-failed" : "s-running"}`}>
                        <span className="dot" />
                        {d.down ? "Falha" : "OK"}
                      </span>
                    </div>
                    <Meter label="" percent={d.total_bytes ? (d.used_bytes / d.total_bytes) * 100 : null} />
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>
      </section>

      <section className="card flush">
        <div className="card-head" style={{ padding: "16px 16px 0" }}>
          <h2>Canais gravando</h2>
        </div>
        {recording.length === 0 ? (
          <Empty>Nenhum canal com DVR ativo</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Canal</th>
                <th>Estado</th>
                <th>Retenção</th>
                <th>Onde</th>
                <th>Servidor</th>
              </tr>
            </thead>
            <tbody>
              {recording.map((c) => (
                <tr key={c.id}>
                  <td>
                    <Link to={`/channels/${c.id}`}>{c.name}</Link>
                  </td>
                  <td>
                    <StateChip state={c.status.state} />
                  </td>
                  <td>{retention(c.dvr.retention_hours)}</td>
                  <td className="mono small">
                    {c.dvr.storage ? (
                      <span className="tag accent">{c.dvr.storage}</span>
                    ) : (
                      c.dvr.path || <span className="muted">pasta padrão do servidor</span>
                    )}
                  </td>
                  <td className="muted">{c.status.worker_id ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      {editing && (
        <Modal title={isNew ? "Novo armazenamento" : `Editar ${editing.id}`} onClose={() => setEditing(null)}>
          <form className="form" onSubmit={save}>
            <ErrorBox error={error} />
            <label>
              Nome
              <input
                value={editing.id}
                disabled={!isNew}
                required
                pattern="[a-z0-9][a-z0-9_-]{0,63}"
                placeholder="principal"
                onChange={(e) => setEditing({ ...editing, id: e.target.value })}
              />
            </label>
            <div className="form-section">
              <div className="form-section-title">Modo</div>
              <div className="mode-picker">
                {MODES.map((m) => (
                  <button
                    type="button"
                    key={m.id}
                    className={`mode-option ${editing.mode === m.id ? "on" : ""}`}
                    onClick={() =>
                      setEditing({
                        ...editing,
                        mode: m.id,
                        disks:
                          m.id === "single"
                            ? editing.disks.slice(0, 1)
                            : editing.disks.length < 2
                              ? [...editing.disks, ""]
                              : editing.disks,
                      })
                    }
                  >
                    {m.title}
                    <span>{m.hint}</span>
                  </button>
                ))}
              </div>
            </div>
            <div className="form-section">
              <div className="form-section-title">Discos (pastas no servidor de mídia)</div>
              {editing.disks.map((d, i) => (
                <div key={i} className="row">
                  <HardDrive size={15} className="muted" />
                  <input
                    className="mono"
                    value={d}
                    placeholder={`/mnt/disk${i + 1}`}
                    required
                    onChange={(e) =>
                      setEditing({
                        ...editing,
                        disks: editing.disks.map((x, j) => (j === i ? e.target.value : x)),
                      })
                    }
                  />
                  <button
                    type="button"
                    className="icon"
                    disabled={editing.disks.length <= (editing.mode === "single" ? 1 : 2)}
                    onClick={() => setEditing({ ...editing, disks: editing.disks.filter((_, j) => j !== i) })}
                  >
                    <X size={15} />
                  </button>
                </div>
              ))}
              {editing.mode !== "single" && (
                <div>
                  <button
                    type="button"
                    className="ghost sm"
                    onClick={() => setEditing({ ...editing, disks: [...editing.disks, ""] })}
                  >
                    <Plus size={14} /> Adicionar disco
                  </button>
                </div>
              )}
              <div className="small muted">
                As pastas precisam existir em todos os servidores que gravam canais deste armazenamento.
              </div>
            </div>
            <div className="form-actions">
              <button type="button" className="secondary" onClick={() => setEditing(null)}>
                Cancelar
              </button>
              <button type="submit">Salvar</button>
            </div>
          </form>
        </Modal>
      )}
    </>
  );
}
