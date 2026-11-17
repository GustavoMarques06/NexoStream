import { Search } from "lucide-react";
import { useState } from "react";
import { qs } from "../api";
import { Empty, ErrorBox, PageHeader } from "../components/ui";
import { dateTime } from "../format";
import { useApi } from "../hooks";
import type { AuditEntry, Page } from "../types";

export function Audit() {
  const [actor, setActor] = useState("");
  const [target, setTarget] = useState("");
  const [cursor, setCursor] = useState<string | null>(null);
  const { data, error } = useApi<Page<AuditEntry>>("/audit-log" + qs({ actor, target, cursor, limit: 100 }));

  const filter = (set: (v: string) => void) => (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      setCursor(null);
      set(e.currentTarget.value);
    }
  };

  return (
    <>
      <PageHeader title="Auditoria">
        <div className="search">
          <Search size={15} />
          <input placeholder="Usuário" onKeyDown={filter(setActor)} />
        </div>
        <div className="search">
          <Search size={15} />
          <input placeholder="Alvo" onKeyDown={filter(setTarget)} />
        </div>
      </PageHeader>
      <ErrorBox error={error} />
      <div className="card flush">
        {data?.items.length === 0 ? (
          <Empty>Sem registros</Empty>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Quando</th>
                <th>Quem</th>
                <th>Ação</th>
                <th>Alvo</th>
                <th>IP</th>
              </tr>
            </thead>
            <tbody>
              {data?.items.map((a) => (
                <tr key={a.id} title={a.details ? JSON.stringify(a.details) : undefined}>
                  <td className="nowrap muted">{dateTime(a.at)}</td>
                  <td>{a.actor}</td>
                  <td>
                    <span className="tag">{a.action}</span>
                  </td>
                  <td>{a.target}</td>
                  <td className="muted small">{a.source_ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {data?.next_cursor && (
        <div className="form-actions" style={{ marginTop: 12 }}>
          <button className="secondary" onClick={() => setCursor(data.next_cursor)}>
            Mais antigos
          </button>
        </div>
      )}
    </>
  );
}
