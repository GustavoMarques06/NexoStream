import Hls from "hls.js";
import { Activity, Grid2x2, Grid3x3, LayoutGrid, Maximize, Volume2, VolumeX, VideoOff } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { attachAudio, graphOf, type AudioGraph } from "../audio";
import { Empty, ErrorBox, PageHeader, StateChip, stateClass } from "../components/ui";
import { VuMeter } from "../components/VuMeter";
import { bitrate } from "../format";
import { useApi } from "../hooks";
import { seed, useLive } from "../live";
import { usePlaybackToken, withToken } from "../playback";
import type { Channel, ChannelStatus, Page } from "../types";

const LAYOUTS = [
  { cols: 2, icon: Grid2x2 },
  { cols: 3, icon: Grid3x3 },
  { cols: 4, icon: LayoutGrid },
];

function Tile({
  channel,
  status,
  meters,
  solo,
  onSolo,
}: {
  channel: Channel;
  status: ChannelStatus;
  meters: boolean;
  solo: boolean;
  onSolo: () => void;
}) {
  const video = useRef<HTMLVideoElement>(null);
  const [graph, setGraph] = useState<AudioGraph | null>(null);
  const [failed, setFailed] = useState(false);
  const playback = usePlaybackToken(channel);
  const src =
    status.state !== "STOPPED" && playback.ready ? withToken(status.outputs?.hls, playback.token) : undefined;

  useEffect(() => {
    const v = video.current;
    if (!v || !src) return;
    setFailed(false);
    if (!graphOf(v)) v.muted = true; // autoplay requires muted until audio is routed
    if (Hls.isSupported()) {
      const hls = new Hls({
        liveSyncDurationCount: 2,
        maxBufferLength: 10,
        backBufferLength: 10,
        capLevelToPlayerSize: true,
      });
      let retry: ReturnType<typeof setTimeout> | undefined;
      hls.on(Hls.Events.ERROR, (_e, d) => {
        if (!d.fatal) return;
        setFailed(true);
        retry = setTimeout(() => hls.loadSource(src), 5000);
      });
      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        setFailed(false);
        void v.play().catch(() => undefined);
      });
      hls.loadSource(src);
      hls.attachMedia(v);
      return () => {
        clearTimeout(retry);
        hls.destroy();
      };
    }
    v.src = src;
    void v.play().catch(() => undefined);
  }, [src]);

  // Meters are enabled by a click on the page (user gesture).
  useEffect(() => {
    const v = video.current;
    if (!v || !meters) return;
    const g = attachAudio(v, solo ? 1 : 0);
    if (g) setGraph(g);
  }, [meters, solo]);

  useEffect(() => {
    if (graph) graph.gain.gain.value = solo ? 1 : 0;
  }, [graph, solo]);

  return (
    <div className={`tile ${stateClass(status.state)} ${solo ? "solo" : ""}`}>
      <video ref={video} playsInline crossOrigin="anonymous" />
      {(!src || failed) && (
        <div className="tile-off">
          <VideoOff size={26} strokeWidth={1.5} />
        </div>
      )}
      {graph && (
        <div className="tile-vu">
          <VuMeter graph={graph} />
        </div>
      )}
      <div className="tile-top">
        <StateChip state={status.state} />
        <div className="spacer" />
        {meters && (
          <button
            className={`pbtn ${solo ? "on" : ""}`}
            onClick={onSolo}
            title={solo ? "Silenciar" : "Ouvir este canal"}
          >
            {solo ? <Volume2 size={15} /> : <VolumeX size={15} />}
          </button>
        )}
      </div>
      <Link to={`/channels/${channel.id}`} className="tile-bottom">
        <span className="truncate">{channel.name}</span>
        <span className="tile-rate">{bitrate(status.input?.bitrate_bps)}</span>
      </Link>
    </div>
  );
}

export function Mosaic() {
  const { data, error } = useApi<Page<Channel>>("/channels?limit=200&enabled=true", 30000);
  const live = useLive();
  const [cols, setCols] = useState(3);
  const [meters, setMeters] = useState(false);
  const [solo, setSolo] = useState<string | null>(null);
  const wrap = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (data) seed(data.items.map((c) => c.status));
  }, [data]);

  const items = (data?.items ?? []).map((c) => ({ c, st: live.status(c.id) ?? c.status }));

  return (
    <>
      <PageHeader title="Mosaico" sub={data ? `${items.length} canais ativos` : undefined}>
        <button className={meters ? "" : "secondary"} onClick={() => setMeters(true)} disabled={meters}>
          <Activity size={15} /> {meters ? "Medidores de áudio ligados" : "Ligar medidores de áudio"}
        </button>
        <div className="segmented">
          {LAYOUTS.map((l) => (
            <button
              key={l.cols}
              className={cols === l.cols ? "on" : ""}
              onClick={() => setCols(l.cols)}
              title={`${l.cols}×${l.cols}`}
            >
              <l.icon size={15} />
            </button>
          ))}
        </div>
        <button
          className="secondary"
          onClick={() => void wrap.current?.requestFullscreen()}
          title="Tela cheia"
        >
          <Maximize size={15} />
        </button>
      </PageHeader>
      <ErrorBox error={error} />
      {data && items.length === 0 && (
        <div className="card">
          <Empty>Nenhum canal ativo</Empty>
        </div>
      )}
      <div ref={wrap} className="mosaic" style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}>
        {items.map(({ c, st }) => (
          <Tile
            key={c.id}
            channel={c}
            status={st}
            meters={meters}
            solo={solo === c.id}
            onSolo={() => setSolo((s) => (s === c.id ? null : c.id))}
          />
        ))}
      </div>
    </>
  );
}
