#!/usr/bin/env bash
# Generates N synthetic MPEG-TS sources over UDP with ffmpeg.
#   ./gen-sources.sh N GROUP_PREFIX PORT [SIZE] [VBITRATE]
#   ./gen-sources.sh 50 239.100.0 5000 1920x1080 4000k
#   ./gen-sources.sh stop
# Env: VCODEC (libx264|mpeg2video|libx265), ACODEC (aac|mp2|ac3), FPS (25), TTL (4)
set -euo pipefail
cd "$(dirname "$0")"
PIDS=sources.pids

if [ "${1:-}" = stop ]; then
  [ -f "$PIDS" ] && xargs -r kill < "$PIDS" 2>/dev/null || true
  rm -f "$PIDS"
  echo "sources stopped"
  exit 0
fi

N="${1:?count}"; PREFIX="${2:?group prefix, e.g. 239.100.0}"; PORT="${3:?port}"
SIZE="${4:-1920x1080}"; VBR="${5:-4000k}"
VCODEC="${VCODEC:-libx264}"; ACODEC="${ACODEC:-aac}"; FPS="${FPS:-25}"; TTL="${TTL:-4}"
GOP=$((FPS * 2))

for i in $(seq 1 "$N"); do
  url="udp://${PREFIX}.${i}:${PORT}?pkt_size=1316&ttl=${TTL}"
  vopts=(-c:v "$VCODEC" -b:v "$VBR" -maxrate "$VBR" -bufsize "$VBR" -g "$GOP")
  [ "$VCODEC" = libx264 ] && vopts+=(-preset ultrafast -tune zerolatency)
  ffmpeg -hide_banner -loglevel error -re \
    -f lavfi -i "testsrc2=size=${SIZE}:rate=${FPS}" \
    -f lavfi -i "sine=frequency=$((300 + i)):sample_rate=48000" \
    "${vopts[@]}" -c:a "$ACODEC" -b:a 128k \
    -f mpegts -mpegts_service_id "$i" "$url" &
  echo $! >> "$PIDS"
done
echo "$N sources started (${PREFIX}.1-${PREFIX}.${N}:${PORT}); PIDs in $PIDS"
