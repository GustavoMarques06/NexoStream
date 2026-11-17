import { KeyRound, LockOpen, Plus, UserCheck, UserX } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "../api";
import { ErrorBox, Modal, PageHeader } from "../components/ui";
import { dateTime } from "../format";
import { useApi } from "../hooks";
import type { Page, User } from "../types";

const ROLES = [
  { id: "admin", label: "Administrador" },
  { id: "operator", label: "Operador" },
  { id: "viewer", label: "Somente leitura" },
];

export function Users() {
  const { data, error, reload } = useApi<Page<User>>("/users");
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ username: "", password: "", role: "operator" });
  const [formError, setFormError] = useState<unknown>(null);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    try {
      await api.post("/users", form);
      setCreating(false);
      setForm({ username: "", password: "", role: "operator" });
      void reload();
    } catch (err) {
      setFormError(err);
    }
  };

  const update = async (u: User, patch: Record<string, unknown>) => {
    setFormError(null);
    try {
      await api.patch(`/users/${u.id}`, patch);
      void reload();
    } catch (err) {
      setFormError(err);
    }
  };

  return (
    <>
      <PageHeader title="Usuários">
        <button onClick={() => setCreating(true)}>
          <Plus size={16} /> Novo usuário
        </button>
      </PageHeader>
      <ErrorBox error={error ?? (!creating ? formError : null)} />
      <div className="card flush">
        <table>
          <thead>
            <tr>
              <th>Usuário</th>
              <th>Perfil</th>
              <th>Situação</th>
              <th>Criado</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {data?.items.map((u) => (
              <tr key={u.id}>
                <td>
                  <div className="row">
                    <span className="avatar">{u.username.slice(0, 2)}</span>
                    <strong>{u.username}</strong>
                  </div>
                </td>
                <td style={{ width: 200 }}>
                  <select value={u.role} onChange={(e) => void update(u, { role: e.target.value })}>
                    {ROLES.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.label}
                      </option>
                    ))}
                  </select>
                </td>
                <td>
                  <span className={`chip ${u.disabled ? "s-stopped" : "s-running"}`}>
                    <span className="dot" />
                    {u.disabled ? "Desativado" : "Ativo"}
                  </span>
                  {u.locked_until && (
                    <span className="chip s-failed" title={`Bloqueada até ${dateTime(u.locked_until)}`}>
                      <span className="dot" />
                      Bloqueada
                    </span>
                  )}
                </td>
                <td className="muted">{dateTime(u.created_at)}</td>
                <td className="actions">
                  {u.locked_until && (
                    <button
                      className="icon"
                      title="Desbloquear"
                      onClick={() => void update(u, { unlock: true })}
                    >
                      <LockOpen size={15} />
                    </button>
                  )}
                  <button
                    className="icon"
                    title="Trocar senha"
                    onClick={() => {
                      const pw = prompt(`Nova senha para ${u.username} (mínimo 10 caracteres):`);
                      if (pw) void update(u, { password: pw });
                    }}
                  >
                    <KeyRound size={15} />
                  </button>
                  <button
                    className="icon"
                    title={u.disabled ? "Reativar" : "Desativar"}
                    onClick={() => void update(u, { disabled: !u.disabled })}
                  >
                    {u.disabled ? <UserCheck size={15} /> : <UserX size={15} />}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {creating && (
        <Modal title="Novo usuário" onClose={() => setCreating(false)}>
          <form className="form" onSubmit={create}>
            <ErrorBox error={formError} />
            <label>
              Usuário
              <input
                value={form.username}
                onChange={(e) => setForm({ ...form, username: e.target.value })}
                required
                autoFocus
              />
            </label>
            <label>
              Senha
              <input
                type="password"
                value={form.password}
                onChange={(e) => setForm({ ...form, password: e.target.value })}
                required
                minLength={10}
                placeholder="Mínimo 10 caracteres"
              />
            </label>
            <label>
              Perfil
              <select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
                {ROLES.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.label}
                  </option>
                ))}
              </select>
            </label>
            <div className="form-actions">
              <button type="button" className="secondary" onClick={() => setCreating(false)}>
                Cancelar
              </button>
              <button type="submit">Criar</button>
            </div>
          </form>
        </Modal>
      )}
    </>
  );
}
