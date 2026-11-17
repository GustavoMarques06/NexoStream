// Web Audio helpers for VU meters. Browsers only allow audio after a user
// gesture, so graphs are created on demand (click). Once a video element is
// routed into the graph, its own volume/mute no longer apply: loudness is
// controlled by the gain node, and the analysers keep measuring even when
// the output is silent (gain 0).

export interface AudioGraph {
  gain: GainNode;
  left: AnalyserNode;
  right: AnalyserNode;
}

let ctx: AudioContext | null = null;
const graphs = new WeakMap<HTMLMediaElement, AudioGraph>();

function context(): AudioContext | null {
  if (!ctx) {
    const AC =
      window.AudioContext ??
      (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!AC) return null;
    ctx = new AC();
  }
  if (ctx.state === "suspended") void ctx.resume();
  return ctx;
}

/** attachAudio routes a media element through gain + L/R analysers. Call it from a user gesture. */
export function attachAudio(el: HTMLMediaElement, gain: number): AudioGraph | null {
  const existing = graphs.get(el);
  if (existing) {
    existing.gain.gain.value = gain;
    return existing;
  }
  const c = context();
  if (!c) return null;
  let src: MediaElementAudioSourceNode;
  try {
    src = c.createMediaElementSource(el);
  } catch {
    return null;
  }
  const split = c.createChannelSplitter(2);
  const left = c.createAnalyser();
  const right = c.createAnalyser();
  left.fftSize = right.fftSize = 1024;
  src.connect(split);
  split.connect(left, 0);
  split.connect(right, 1);
  const g = c.createGain();
  g.gain.value = gain;
  src.connect(g);
  g.connect(c.destination);
  el.muted = false;
  el.volume = 1;
  const graph = { gain: g, left, right };
  graphs.set(el, graph);
  return graph;
}

export function graphOf(el: HTMLMediaElement | null): AudioGraph | undefined {
  return el ? graphs.get(el) : undefined;
}

const buf = new Float32Array(1024);

/** level returns the RMS level of an analyser in dBFS (−∞..0). */
export function level(a: AnalyserNode): number {
  a.getFloatTimeDomainData(buf);
  let sum = 0;
  for (let i = 0; i < buf.length; i++) sum += (buf[i] ?? 0) * (buf[i] ?? 0);
  const rms = Math.sqrt(sum / buf.length);
  return rms > 0 ? 20 * Math.log10(rms) : -Infinity;
}
