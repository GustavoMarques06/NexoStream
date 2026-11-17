import Hls, { type Level } from "hls.js";
import {
  Info,
  Loader2,
  Maximize,
  Minimize,
  Pause,
  PictureInPicture2,
  Play,
  RefreshCw,
  Settings,
  Volume2,
  VolumeX,
  VideoOff,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { attachAudio, type AudioGraph } from "../audio";
import { bitrate } from "../format";
import { VuMeter } from "./VuMeter";

const VOLUME_KEY = "nexostream.player.volume";

function savedVolume(): number {
  try {
    const v = Number(localStorage.getItem(VOLUME_KEY));
    return Number.isFinite(v) && v > 0 && v <= 1 ? v : 0.8;
  } catch {
    return 0.8;
  }
}

interface Stats {
  width: number;
  height: number;
  levelBitrate: number;
  bandwidth: number;
  buffer: number;
  latency: number | null;
  dropped: number;
  decoded: number;
  // DVR: seekable window and the wall-clock time being played.
  seekStart: number;
  seekEnd: number;
  current: number;
  clock: Date | null;
}

type Phase = "loading" | "playing" | "buffering" | "error";

/**
 * Player previews a live HLS output with hls.js (native HLS on Safari).
 * Custom controls: live edge, volume, quality, stats, PiP, fullscreen.
 * Shortcuts: space/k play, m mute, f fullscreen, i stats, l live.
 */
export function Player({
  src,
  mode = "live",
  onGoLive,
}: {
  src?: string;
  /** dvr: timeshift/catch-up playback with a seek bar. */
  mode?: "live" | "dvr";
  onGoLive?: () => void;
}) {
  const wrap = useRef<HTMLDivElement>(null);
  const video = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);
  const [phase, setPhase] = useState<Phase>("loading");
  const [paused, setPaused] = useState(false);
  const [muted, setMutedState] = useState(true);
  const [graph, setGraph] = useState<AudioGraph | null>(null);
  const [volume, setVolume] = useState(savedVolume);
  const [levels, setLevels] = useState<Level[]>([]);
  const [level, setLevel] = useState(-1);
  const [showStats, setShowStats] = useState(false);
  const [showQuality, setShowQuality] = useState(false);
  const [stats, setStats] = useState<Stats | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [idle, setIdle] = useState(false);
  const [retryIn, setRetryIn] = useState(0);
  const [attempt, setAttempt] = useState(0);
  const idleTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Load the stream (and reload on reconnect attempts).
  useEffect(() => {
    const v = video.current;
    if (!v || !src) return;
    setPhase("loading");
    if (Hls.isSupported()) {
      const hls = new Hls({
        liveSyncDurationCount: 2,
        liveMaxLatencyDurationCount: 6,
        maxLiveSyncPlaybackRate: 1.15,
        enableWorker: true,
        backBufferLength: 30,
        manifestLoadingMaxRetry: 2,
      });
      hlsRef.current = hls;
      hls.on(Hls.Events.MANIFEST_PARSED, (_e, d) => {
        setLevels(d.levels);
        void v.play().catch(() => setPaused(true));
      });
      hls.on(Hls.Events.LEVEL_SWITCHED, (_e, d) => setLevel(hls.autoLevelEnabled ? -1 : d.level));
      hls.on(Hls.Events.ERROR, (_e, data) => {
        if (!data.fatal) return;
        if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
          hls.recoverMediaError();
          return;
        }
        setPhase("error");
        hls.destroy();
        hlsRef.current = null;
        const wait = Math.min(15, 2 ** Math.min(attempt, 4));
        setRetryIn(wait);
      });
      hls.loadSource(src);
      hls.attachMedia(v);
      return () => {
        hls.destroy();
        hlsRef.current = null;
      };
    }
    if (v.canPlayType("application/vnd.apple.mpegurl")) {
      v.src = src;
      void v.play().catch(() => setPaused(true));
      return;
    }
    setPhase("error");
  }, [src, attempt]);

  // Reconnect countdown.
  useEffect(() => {
    if (phase !== "error" || retryIn <= 0) return;
    const t = setTimeout(() => {
      if (retryIn <= 1) setAttempt((a) => a + 1);
      setRetryIn((r) => r - 1);
    }, 1000);
    return () => clearTimeout(t);
  }, [phase, retryIn]);

  // Media element events.
  useEffect(() => {
    const v = video.current;
    if (!v) return;
    const on = (name: string, fn: () => void) => {
      v.addEventListener(name, fn);
      return () => v.removeEventListener(name, fn);
    };
    const offs = [
      on("playing", () => {
        setPhase("playing");
        setPaused(false);
        setAttempt(0);
      }),
      on("waiting", () => setPhase((p) => (p === "error" ? p : "buffering"))),
      on("pause", () => setPaused(true)),
      on("play", () => setPaused(false)),
    ];
    return () => offs.forEach((f) => f());
  }, [src]);

  useEffect(() => {
    const v = video.current;
    if (!v) return;
    if (graph) {
      // Routed through Web Audio: the gain node controls loudness.
      v.muted = false;
      v.volume = 1;
      graph.gain.gain.value = muted ? 0 : volume;
    } else {
      v.muted = muted;
      v.volume = volume;
    }
    try {
      localStorage.setItem(VOLUME_KEY, String(volume));
    } catch {
      // per-viewer convenience only
    }
  }, [muted, volume, graph]);

  // setMuted runs inside user gestures: the first unmute creates the audio
  // graph (VU meter) while the browser allows audio.
  const setMuted = useCallback(
    (next: boolean | ((m: boolean) => boolean)) => {
      const m = typeof next === "function" ? next(muted) : next;
      if (!m && !graph && video.current) {
        const g = attachAudio(video.current, volume);
        if (g) setGraph(g);
      }
      setMutedState(m);
    },
    [muted, graph, volume],
  );

  // Stats sampling (also drives the live/latency badge).
  useEffect(() => {
    const t = setInterval(() => {
      const v = video.current;
      if (!v) return;
      const hls = hlsRef.current;
      let buffer = 0;
      for (let i = 0; i < v.buffered.length; i++) {
        if (v.currentTime >= v.buffered.start(i) && v.currentTime <= v.buffered.end(i))
          buffer = v.buffered.end(i) - v.currentTime;
      }
      const q = v.getVideoPlaybackQuality?.();
      const lv = hls && hls.currentLevel >= 0 ? hls.levels[hls.currentLevel] : undefined;
      setStats({
        width: v.videoWidth,
        height: v.videoHeight,
        levelBitrate: lv?.bitrate ?? 0,
        bandwidth: hls?.bandwidthEstimate ?? 0,
        buffer,
        latency: hls?.latency ?? null,
        dropped: q?.droppedVideoFrames ?? 0,
        decoded: q?.totalVideoFrames ?? 0,
        seekStart: v.seekable.length ? v.seekable.start(0) : 0,
        seekEnd: v.seekable.length ? v.seekable.end(v.seekable.length - 1) : 0,
        current: v.currentTime,
        clock: hls?.playingDate ?? null,
      });
    }, 1000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    const f = () => setFullscreen(document.fullscreenElement === wrap.current);
    document.addEventListener("fullscreenchange", f);
    return () => document.removeEventListener("fullscreenchange", f);
  }, []);

  const togglePlay = useCallback(() => {
    const v = video.current;
    if (!v) return;
    if (v.paused) void v.play().catch(() => setPaused(true));
    else v.pause();
  }, []);
  const goLive = useCallback(() => {
    if (mode === "dvr" && onGoLive) {
      onGoLive();
      return;
    }
    const v = video.current;
    const hls = hlsRef.current;
    if (!v) return;
    if (hls?.liveSyncPosition) v.currentTime = hls.liveSyncPosition;
    else if (v.seekable.length) v.currentTime = v.seekable.end(v.seekable.length - 1) - 1;
    void v.play().catch(() => setPaused(true));
  }, [mode, onGoLive]);
  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) void document.exitFullscreen();
    else void wrap.current?.requestFullscreen();
  }, []);
  const togglePip = useCallback(() => {
    const v = video.current;
    if (!v) return;
    if (document.pictureInPictureElement) void document.exitPictureInPicture();
    else void v.requestPictureInPicture?.();
  }, []);
  const chooseLevel = (l: number) => {
    const hls = hlsRef.current;
    if (hls) hls.currentLevel = l;
    setLevel(l);
    setShowQuality(false);
  };

  const onKey = (e: React.KeyboardEvent) => {
    const k = e.key.toLowerCase();
    if (k === " " || k === "k") togglePlay();
    else if (k === "m") setMuted((m) => !m);
    else if (k === "f") toggleFullscreen();
    else if (k === "i") setShowStats((s) => !s);
    else if (k === "l") goLive();
    else return;
    e.preventDefault();
  };

  const poke = () => {
    setIdle(false);
    if (idleTimer.current) clearTimeout(idleTimer.current);
    idleTimer.current = setTimeout(() => setIdle(true), 2500);
  };

  if (!src) {
    return (
      <div className="player">
        <div className="player-off">
          <VideoOff size={34} strokeWidth={1.5} />
          <span>Sem saída disponível</span>
        </div>
      </div>
    );
  }

  const latency = stats?.latency ?? null;
  const behind = latency !== null && latency > 25;
  const qualityLabel = level < 0 ? "Auto" : `${levels[level]?.height ?? "?"}p`;

  return (
    <div
      ref={wrap}
      className={`player ${idle && !paused && phase === "playing" ? "idle" : ""}`}
      tabIndex={0}
      onKeyDown={onKey}
      onMouseMove={poke}
      onMouseLeave={() => setIdle(true)}
    >
      <video
        ref={video}
        playsInline
        crossOrigin="anonymous"
        onClick={togglePlay}
        onDoubleClick={toggleFullscreen}
      />
      {graph && (
        <div className="player-vu">
          <VuMeter graph={graph} />
        </div>
      )}

      {(phase === "loading" || phase === "buffering") && (
        <div className="player-center">
          <Loader2 size={38} className="spin" />
        </div>
      )}
      {phase === "error" && (
        <div className="player-center column">
          <VideoOff size={34} strokeWidth={1.5} />
          <span>Sem sinal no player</span>
          <button className="secondary sm" onClick={() => setAttempt((a) => a + 1)}>
            <RefreshCw size={13} /> {retryIn > 0 ? `Reconectando em ${retryIn}s` : "Tentar agora"}
          </button>
        </div>
      )}
      {paused && phase === "playing" && (
        <button className="player-big-play" onClick={togglePlay} aria-label="Reproduzir">
          <Play size={30} fill="currentColor" />
        </button>
      )}
      {muted && phase === "playing" && (
        <button className="player-unmute" onClick={() => setMuted(false)}>
          <VolumeX size={14} /> Ativar som
        </button>
      )}

      {showStats && stats && (
        <div className="player-stats">
          <div>
            <span>Resolução</span>
            {stats.width ? `${stats.width}×${stats.height}` : "—"}
          </div>
          <div>
            <span>Bitrate</span>
            {stats.levelBitrate ? bitrate(stats.levelBitrate) : "—"}
          </div>
          <div>
            <span>Banda</span>
            {stats.bandwidth ? bitrate(stats.bandwidth) : "—"}
          </div>
          <div>
            <span>Buffer</span>
            {stats.buffer.toFixed(1)} s
          </div>
          <div>
            <span>Latência</span>
            {latency !== null ? `${latency.toFixed(1)} s` : "—"}
          </div>
          <div>
            <span>Frames perdidos</span>
            {stats.dropped} / {stats.decoded}
          </div>
        </div>
      )}

      <div className="player-bar" onClick={(e) => e.stopPropagation()}>
        <button className="pbtn" onClick={togglePlay} title={paused ? "Reproduzir (k)" : "Pausar (k)"}>
          {paused ? <Play size={18} /> : <Pause size={18} />}
        </button>
        <button
          className={`live-pill ${mode === "dvr" || behind || paused ? "behind" : ""}`}
          onClick={goLive}
          title="Ir para o ao vivo (l)"
        >
          <span className="live-pill-dot" />
          {mode === "dvr" ? "IR PARA O AO VIVO" : "AO VIVO"}
          {mode === "live" && latency !== null && (
            <span className="live-pill-lat">{latency.toFixed(0)}s</span>
          )}
        </button>
        {mode === "dvr" && stats && stats.seekEnd > stats.seekStart && (
          <div className="pseek">
            <input
              type="range"
              min={stats.seekStart}
              max={stats.seekEnd}
              step={1}
              value={stats.current}
              onChange={(e) => {
                if (video.current) video.current.currentTime = Number(e.target.value);
              }}
              aria-label="Posição na gravação"
            />
            <span className="pclock">{stats.clock ? stats.clock.toLocaleTimeString("pt-BR") : ""}</span>
          </div>
        )}
        <div className="pvol">
          <button className="pbtn" onClick={() => setMuted((m) => !m)} title="Som (m)">
            {muted || volume === 0 ? <VolumeX size={18} /> : <Volume2 size={18} />}
          </button>
          <input
            type="range"
            min={0}
            max={1}
            step={0.05}
            value={muted ? 0 : volume}
            onChange={(e) => {
              const v = Number(e.target.value);
              setVolume(v);
              setMuted(v === 0);
            }}
            aria-label="Volume"
          />
        </div>
        <div className="spacer" />
        {levels.length > 1 && (
          <div className="pmenu-wrap">
            <button className="pbtn text" onClick={() => setShowQuality((s) => !s)} title="Qualidade">
              <Settings size={16} /> {qualityLabel}
            </button>
            {showQuality && (
              <div className="pmenu">
                <button className={level < 0 ? "on" : ""} onClick={() => chooseLevel(-1)}>
                  Auto
                </button>
                {levels.map((l, i) => (
                  <button key={i} className={level === i ? "on" : ""} onClick={() => chooseLevel(i)}>
                    {l.height}p · {bitrate(l.bitrate)}
                  </button>
                ))}
              </div>
            )}
          </div>
        )}
        <button
          className={`pbtn ${showStats ? "on" : ""}`}
          onClick={() => setShowStats((s) => !s)}
          title="Estatísticas (i)"
        >
          <Info size={17} />
        </button>
        {"pictureInPictureEnabled" in document && (
          <button className="pbtn" onClick={togglePip} title="Picture-in-picture">
            <PictureInPicture2 size={17} />
          </button>
        )}
        <button className="pbtn" onClick={toggleFullscreen} title="Tela cheia (f)">
          {fullscreen ? <Minimize size={17} /> : <Maximize size={17} />}
        </button>
      </div>
    </div>
  );
}
