import { Copy, FileJson, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "../api";
import { ErrorBox, Modal, PageHeader } from "../components/ui";
import { ago, dateTime } from "../format";
import { useApi } from "../hooks";
import type { ApiToken, Page } from "../types";

const SCOPES = [
  "channels:read",
  "channels:write",
  "channels:operate",
  "dvr:read",
  "dvr:write",
  "workers:read",
  "workers:write",
  "audit:read",
];

export function Tokens() {
  const { data, error, reload } = useApi<Page<ApiToken>>("/api-tokens");
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<string[]>(["channels:read"]);
  const [days, setDays] = useState(365);
  const [secret, setSecret] = useState<string | null>(null);
  const [formError, setFormError] = useState<unknown>(null);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    try {
      const res = await api.post<{ token: string }>("/api-tokens", { name, scopes, expires_in_days: days });
      setSecret(res.token);
      setCreating(false);
      setName("");
      void reload();
    } catch (err) {
      setFormError(err);
    }
  };

  const revoke = async (t: ApiToken) => {
    if (!confirm(`Revogar o token "${t.name}"?`)) return;
    await api.del(`/api-tokens/${t.id}`);
    void reload();
  };

  return (
    <>
      <PageHeader title="API e tokens">
        <a
          className="btn secondary"
          href="/api/v1/openapi.json"
          target="_blank"
          rel="noreferrer"
          style={{ color: "var(--text)", background: "var(--surface-2)", border: "1px solid var(--border)" }}
        >
          <FileJson size={15} /> OpenAPI
        </a>
        <button onClick={() => setCreating(true)}>
          <Plus size={16} /> Novo token
        </button>
      </PageHeader>
      {secret && (
        <div className="alert info">
          <div style={{ flex: 1 }}>
            <strong>Copie o token agora. Ele não será exibido de novo.</strong>
            <div className="secret">{secret}</div>
            <div className="row">
              <button className="secondary sm" onClick={() => void navigator.clipboard?.writeText(secret)}>
                <Copy size={13} /> Copiar
              </button>
              <button className="ghost sm" onClick={() => setSecret(null)}>
                Fechar
              </button>
            </div>
          </div>
        </div>
      )}
      <ErrorBox error={error} />
      <div className="card flush">
        <table>
          <thead>
            <tr>
              <th>Nome</th>
              <th>Escopos</th>
              <th>Expira</th>
              <th>Último uso</th>
              <th>Situação</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {data?.items.map((t) => (
              <tr key={t.id}>
                <td>
                  <strong>{t.name}</strong>
                  <div className="mono muted">{t.prefix}…</div>
                </td>
                <td>
                  <div className="row" style={{ flexWrap: "wrap", gap: 4 }}>
                    {t.scopes.map((s) => (
                      <span key={s} className="tag">
                        {s}
                      </span>
                    ))}
                  </div>
                </td>
                <td className="muted">{dateTime(t.expires_at)}</td>
                <td className="muted">{t.last_used_at ? ago(t.last_used_at) : "nunca"}</td>
                <td>
                  <span className={`chip ${t.revoked_at ? "s-stopped" : "s-running"}`}>
                    <span className="dot" />
                    {t.revoked_at ? "Revogado" : "Ativo"}
                  </span>
                </td>
                <td className="actions">
                  {!t.revoked_at && (
                    <button className="icon" title="Revogar" onClick={() => void revoke(t)}>
                      <Trash2 size={15} />
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {creating && (
        <Modal title="Novo token" onClose={() => setCreating(false)}>
          <form className="form" onSubmit={create}>
            <ErrorBox error={formError} />
            <div className="fields">
              <label>
                Nome
                <input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  required
                  placeholder="middleware"
                  autoFocus
                />
              </label>
              <label>
                Validade (dias)
                <input
                  type="number"
                  min={1}
                  max={3650}
                  value={days}
                  onChange={(e) => setDays(Number(e.target.value))}
                />
              </label>
            </div>
            <div className="form-section">
              <div className="form-section-title">Escopos</div>
              <div className="fields">
                {SCOPES.map((s) => (
                  <label key={s} className="check">
                    <input
                      type="checkbox"
                      checked={scopes.includes(s)}
                      onChange={(e) =>
                        setScopes(e.target.checked ? [...scopes, s] : scopes.filter((x) => x !== s))
                      }
                    />
                    <span className="mono">{s}</span>
                  </label>
                ))}
              </div>
            </div>
            <div className="form-actions">
              <button type="button" className="secondary" onClick={() => setCreating(false)}>
                Cancelar
              </button>
              <button type="submit" disabled={scopes.length === 0}>
                Criar token
              </button>
            </div>
          </form>
        </Modal>
      )}
    </>
  );
}
