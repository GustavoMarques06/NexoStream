import { useEffect, useRef } from "react";
import { level, type AudioGraph } from "../audio";

const FLOOR = -60; // dBFS shown as empty

function pct(db: number) {
  if (!Number.isFinite(db)) return 0;
  return Math.max(0, Math.min(100, ((db - FLOOR) / -FLOOR) * 100));
}

/** VuMeter draws L/R level bars from an audio graph (requestAnimationFrame, no React re-render). */
export function VuMeter({ graph, vertical = true }: { graph: AudioGraph; vertical?: boolean }) {
  const l = useRef<HTMLSpanElement>(null);
  const r = useRef<HTMLSpanElement>(null);
  const peakL = useRef(0);
  const peakR = useRef(0);

  useEffect(() => {
    let raf = 0;
    const tick = () => {
      let dl = level(graph.left);
      let dr = level(graph.right);
      if (!Number.isFinite(dr) && Number.isFinite(dl)) dr = dl; // mono source
      if (!Number.isFinite(dl) && Number.isFinite(dr)) dl = dr;
      // fast attack, slow release
      peakL.current = Math.max(pct(dl), peakL.current - 2.5);
      peakR.current = Math.max(pct(dr), peakR.current - 2.5);
      // The cover hides the part of the fixed green-yellow-red track above the level.
      const prop = vertical ? "height" : "width";
      if (l.current) l.current.style[prop] = `${100 - peakL.current}%`;
      if (r.current) r.current.style[prop] = `${100 - peakR.current}%`;
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [graph, vertical]);

  return (
    <div className={`vu ${vertical ? "vertical" : ""}`} title="Nível de áudio (L/R)">
      <div className="vu-track">
        <span ref={l} className="vu-cover" />
      </div>
      <div className="vu-track">
        <span ref={r} className="vu-cover" />
      </div>
    </div>
  );
}
