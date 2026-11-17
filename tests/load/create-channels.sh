#!/usr/bin/env bash
# Creates (or removes) N load-test channels through the API.
#   ./create-channels.sh N GROUP_PREFIX PORT MODE [PROFILE]
#   ./create-channels.sh 50 239.100.0 5000 passthrough
#   ./create-channels.sh 10 239.101.0 5000 transcode hd
#   ./create-channels.sh delete N
# Env: NEXOSTREAM_API (https://host/api/v1), NEXOSTREAM_TOKEN (nxs_...), IFACE (optional)
set -euo pipefail
: "${NEXOSTREAM_API:?set NEXOSTREAM_API}" "${NEXOSTREAM_TOKEN:?set NEXOSTREAM_TOKEN}"
H=(-H "Authorization: Bearer ${NEXOSTREAM_TOKEN}" -H "Content-Type: application/json")

if [ "${1:-}" = delete ]; then
  N="${2:?count}"
  for i in $(seq 1 "$N"); do
    id=$(printf "carga-%03d" "$i")
    curl -fsS -X POST "${H[@]}" "$NEXOSTREAM_API/channels/$id/stop" >/dev/null || true
  done
  sleep 10
  for i in $(seq 1 "$N"); do
    id=$(printf "carga-%03d" "$i")
    curl -fsS -X DELETE "${H[@]}" "$NEXOSTREAM_API/channels/$id" >/dev/null || echo "could not delete $id"
  done
  exit 0
fi

N="${1:?count}"; PREFIX="${2:?group prefix}"; PORT="${3:?port}"; MODE="${4:-passthrough}"; PROFILE="${5:-}"
profiles="[]"
[ -n "$PROFILE" ] && profiles="[\"$PROFILE\"]"
for i in $(seq 1 "$N"); do
  id=$(printf "carga-%03d" "$i")
  body=$(jq -n --arg id "$id" --arg url "udp://${PREFIX}.${i}:${PORT}" --arg mode "$MODE" \
    --arg iface "${IFACE:-}" --argjson profiles "$profiles" \
    '{id:$id, name:("Carga " + $id), group:"Carga", mode:$mode, profiles:$profiles,
      sources:[{priority:1, url:$url} + (if $iface != "" then {interface:$iface} else {} end)],
      outputs:{hls:true}}')
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${H[@]}" -H "Idempotency-Key: load-$id" \
    -d "$body" "$NEXOSTREAM_API/channels")
  echo "$id -> $code"
done
