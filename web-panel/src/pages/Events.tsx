import { useState } from "react";
import { Link } from "react-router-dom";
import { qs } from "../api";
import { Empty, ErrorBox, eventLook, PageHeader } from "../components/ui";
import { describeEvent, EVENT_LABEL } from "../format";
import { useApi } from "../hooks";
import { useLiveEvents } from "../live";
import type { NsEvent, Page } from "../types";

export function Events() {
  const [type, setType] = useState("");
  const [cursor, setCursor] = useState<string | null>(null);
  const { data, error, reload } = useApi<Page<NsEvent>>("/events" + qs({ type, cursor, limit: 100 }));
  useLiveEvents(() => {
    if (!cursor) void reload();
  });

  return (
    <>
      <PageHeader title="Eventos">
        <select
          style={{ width: 220 }}
          value={type}
          onChange={(e) => {
            setCursor(null);
            setType(e.target.value);
          }}
        >
          <option value="">Todos os tipos</option>
          {Object.entries(EVENT_LABEL).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
      </PageHeader>
      <ErrorBox error={error} />
      <div className="card">
        {data?.items.length === 0 && <Empty>Sem eventos</Empty>}
        <div className="list">
          {data?.items.map((e) => {
            const look = eventLook(e);
            return (
              <div key={e.id} className="list-item">
                <span className={`feed-icon ${look.cls}`}>
                  <look.icon size={14} />
                </span>
                <div className="feed-body">
                  <div className="feed-title">
                    {e.channel_id ? (
                      <Link to={`/channels/${e.channel_id}`}>{e.channel_id}</Link>
                    ) : (
                      e.worker_id
                    )}
                    <span className="muted"> · {EVENT_LABEL[e.type] ?? e.type}</span>
                  </div>
                  <div className="feed-sub">{describeEvent(e)}</div>
                </div>
                <span className="time">{new Date(e.at).toLocaleString("pt-BR")}</span>
              </div>
            );
          })}
        </div>
      </div>
      <div className="form-actions" style={{ marginTop: 12 }}>
        {cursor && (
          <button className="secondary" onClick={() => setCursor(null)}>
            Mais recentes
          </button>
        )}
        {data?.next_cursor && (
          <button className="secondary" onClick={() => setCursor(data.next_cursor)}>
            Mais antigos
          </button>
        )}
      </div>
    </>
  );
}
