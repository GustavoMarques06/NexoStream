#!/usr/bin/env bash
# Samples a worker's /metrics every 15 s and prints CSV.
#   ./collect.sh WORKER_HOST:9500 DURATION_SECONDS > out.csv
set -euo pipefail
TARGET="${1:?worker host:port}"; DURATION="${2:-3600}"; STEP="${STEP:-15}"

sum() { awk -v m="$1" '$1 ~ "^"m"({|$| )" && $1 !~ /^#/ {s += $NF} END {printf "%.0f", s}'; }
count_state() { awk -v s="$1" '$1 ~ "^nexostream_channel_state{" && $1 ~ "state=\""s"\"" {c += $NF} END {printf "%.0f", c}'; }

echo "ts,channels,up,failed,on_backup,input_bytes_total,cc_errors_total,restarts_total,dropped_bytes_total,process_cpu_seconds,process_rss_bytes"
end=$(( $(date +%s) + DURATION ))
while [ "$(date +%s)" -lt "$end" ]; do
  m=$(curl -fsS "http://$TARGET/metrics" || true)
  printf "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n" "$(date -u +%FT%TZ)" \
    "$(echo "$m" | sum nexostream_worker_channels)" \
    "$(echo "$m" | sum nexostream_channel_up)" \
    "$(echo "$m" | count_state FAILED)" \
    "$(echo "$m" | count_state ON_BACKUP)" \
    "$(echo "$m" | sum nexostream_channel_input_bytes_total)" \
    "$(echo "$m" | sum nexostream_channel_cc_errors_total)" \
    "$(echo "$m" | sum nexostream_pipeline_restarts_total)" \
    "$(echo "$m" | sum nexostream_channel_input_dropped_bytes_total)" \
    "$(echo "$m" | sum process_cpu_seconds_total)" \
    "$(echo "$m" | sum process_resident_memory_bytes)"
  sleep "$STEP"
done
