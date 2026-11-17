import { Pencil, Plus, Trash2 } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "../api";
import { ErrorBox, Modal, PageHeader } from "../components/ui";
import { bitrate } from "../format";
import { useApi } from "../hooks";
import { useCan } from "../me";
import type { Page, Profile } from "../types";

const EMPTY: Profile = {
  id: "",
  encoder: "auto",
  video: {
    codec: "h264",
    profile: "high",
    width: 1280,
    height: 720,
    bitrate_bps: 3000000,
    max_bitrate_bps: 3000000,
    gop_seconds: 2,
    deinterlace: "auto",
    preset: "veryfast",
  },
  audio: { codec: "aac", bitrate_bps: 128000, sample_rate: 48000, channels: 2 },
};

export function Profiles() {
  const { data, error, reload } = useApi<Page<Profile>>("/transcode-profiles");
  const [editing, setEditing] = useState<Profile | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [formError, setFormError] = useState<unknown>(null);
  const canWrite = useCan("channels:write");

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!editing) return;
    setFormError(null);
    try {
      if (isNew) await api.post("/transcode-profiles", editing);
      else
        await api.patch(`/transcode-profiles/${editing.id}`, {
          video: editing.video,
          audio: editing.audio,
          encoder: editing.encoder,
        });
      setEditing(null);
      void reload();
    } catch (err) {
      setFormError(err);
    }
  };

  const remove = async (p: Profile) => {
    if (!confirm(`Remover o perfil ${p.id}?`)) return;
    try {
      await api.del(`/transcode-profiles/${p.id}`);
      void reload();
    } catch (err) {
      setFormError(err);
    }
  };

  const v = (k: keyof Profile["video"]) => (
    <input
      type={typeof EMPTY.video[k] === "number" ? "number" : "text"}
      step="any"
      value={editing ? String(editing.video[k] ?? "") : ""}
      onChange={(e) =>
        editing &&
        setEditing({
          ...editing,
          video: {
            ...editing.video,
            [k]: typeof EMPTY.video[k] === "number" ? Number(e.target.value) : e.target.value,
          },
        })
      }
    />
  );
  const a = (k: keyof Profile["audio"]) => (
    <input
      type="number"
      value={editing ? String(editing.audio[k]) : ""}
      onChange={(e) =>
        editing && setEditing({ ...editing, audio: { ...editing.audio, [k]: Number(e.target.value) } })
      }
    />
  );

  return (
    <>
      <PageHeader title="Transcodificação" sub="Perfis de saída H.264/AAC">
        {canWrite && (
          <button
            onClick={() => {
              setIsNew(true);
              setEditing({ ...EMPTY, video: { ...EMPTY.video }, audio: { ...EMPTY.audio } });
            }}
          >
            <Plus size={16} /> Novo perfil
          </button>
        )}
      </PageHeader>
      <ErrorBox error={error ?? (!editing ? formError : null)} />
      <div className="card flush">
        <table>
          <thead>
            <tr>
              <th>Perfil</th>
              <th>Resolução</th>
              <th>Vídeo</th>
              <th>Áudio</th>
              <th>GOP</th>
              <th>Encoder</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {data?.items.map((p) => (
              <tr key={p.id}>
                <td>
                  <span className="tag accent">{p.id}</span>
                </td>
                <td>
                  {p.video.width}×{p.video.height}
                </td>
                <td>
                  H.264 {p.video.profile} {bitrate(p.video.bitrate_bps)}
                </td>
                <td>
                  AAC {bitrate(p.audio.bitrate_bps)} {p.audio.sample_rate / 1000} kHz {p.audio.channels}ch
                </td>
                <td>{p.video.gop_seconds} s</td>
                <td>
                  <span className="tag">{p.encoder.toUpperCase()}</span>
                </td>
                <td className="actions">
                  {canWrite && (
                    <>
                      <button
                        className="icon"
                        title="Editar"
                        onClick={() => {
                          setIsNew(false);
                          setEditing(structuredClone(p));
                        }}
                      >
                        <Pencil size={15} />
                      </button>
                      <button className="icon" title="Remover" onClick={() => void remove(p)}>
                        <Trash2 size={15} />
                      </button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing && (
        <Modal title={isNew ? "Novo perfil" : `Editar ${editing.id}`} onClose={() => setEditing(null)}>
          <form className="form" onSubmit={save}>
            <ErrorBox error={formError} />
            <div className="fields">
              <label>
                Identificador
                <input
                  value={editing.id}
                  disabled={!isNew}
                  onChange={(e) => setEditing({ ...editing, id: e.target.value })}
                  required
                />
              </label>
              <label>
                Encoder
                <select
                  value={editing.encoder}
                  onChange={(e) => setEditing({ ...editing, encoder: e.target.value as Profile["encoder"] })}
                >
                  <option value="auto">auto</option>
                  <option value="cpu">cpu (libx264)</option>
                  <option value="nvenc">nvenc (NVIDIA)</option>
                  <option value="qsv">qsv (Intel)</option>
                </select>
              </label>
              <label>Perfil H.264{v("profile")}</label>
              <label>Largura{v("width")}</label>
              <label>Altura{v("height")}</label>
              <label>Bitrate de vídeo (bps){v("bitrate_bps")}</label>
              <label>Bitrate máximo (bps){v("max_bitrate_bps")}</label>
              <label>GOP (s){v("gop_seconds")}</label>
              <label>Desentrelaçar (auto/on/off){v("deinterlace")}</label>
              <label>Preset{v("preset")}</label>
              <label>Bitrate de áudio (bps){a("bitrate_bps")}</label>
              <label>Amostragem (Hz){a("sample_rate")}</label>
              <label>Canais de áudio{a("channels")}</label>
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
