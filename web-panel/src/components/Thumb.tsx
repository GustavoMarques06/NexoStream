import { VideoOff } from "lucide-react";
import { useEffect, useState } from "react";
import { usePlaybackToken, withToken } from "../playback";
import type { Channel } from "../types";

/** Thumb shows a recent picture of the channel, refreshed every 15 s. */
export function Thumb({ c }: { c: Channel }) {
  const playback = usePlaybackToken(c);
  const [tick, setTick] = useState(() => Math.floor(Date.now() / 15000));
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const t = setInterval(() => setTick(Math.floor(Date.now() / 15000)), 15000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => setFailed(false), [tick]);
  const url = c.status.state !== "STOPPED" && playback.ready ? c.status.thumbnail_url : undefined;
  const src = url ? withToken(`${url}?t=${tick}`, playback.token) : undefined;
  return (
    <div className="ch-thumb">
      {src && !failed ? (
        <img src={src} alt="" loading="lazy" onError={() => setFailed(true)} />
      ) : (
        <VideoOff size={22} />
      )}
    </div>
  );
}
