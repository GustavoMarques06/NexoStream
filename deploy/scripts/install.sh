#!/usr/bin/env bash
# Installs NexoStream on an Ubuntu Server LTS node (docs/deployment.md).
#
#   sudo ./deploy/scripts/install.sh worker   # Media Node
#   sudo ./deploy/scripts/install.sh control  # API + controller
#   sudo ./deploy/scripts/install.sh all      # lab: everything on one host
#
# Expects the binaries in ./bin (make build-linux) and, for "control", the
# panel in ./web-panel/dist. Idempotent: never overwrites existing .env files.
set -euo pipefail

ROLE="${1:-}"
case "$ROLE" in worker|control|all) ;; *) echo "usage: $0 worker|control|all" >&2; exit 2 ;; esac
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
cd "$(dirname "$0")/../.."

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

log "user and directories"
id nexostream >/dev/null 2>&1 || useradd --system --home /var/lib/nexostream --shell /usr/sbin/nologin nexostream
install -d -o root -g nexostream -m 750 /etc/nexostream
install -d -o nexostream -g nexostream -m 755 /var/lib/nexostream /var/lib/nexostream/hls /var/lib/nexostream/worker /var/log/nexostream
install -d -o root -g nexostream -m 755 /var/lib/nexostream/overlays
install -d -o nexostream -g nexostream -m 755 /srv/nexostream /srv/nexostream/dvr

install_env() {
  local name="$1"
  if [ ! -f "/etc/nexostream/$name.env" ]; then
    install -o root -g nexostream -m 640 "deploy/configs/$name.env.example" "/etc/nexostream/$name.env"
    log "created /etc/nexostream/$name.env — edit it before starting the service"
  fi
}

install_unit() {
  install -m 644 "deploy/systemd/$1.service" /etc/systemd/system/
}

if [ "$ROLE" = worker ] || [ "$ROLE" = all ]; then
  log "worker packages"
  apt-get update -q
  apt-get install -y -q ffmpeg nginx prometheus-node-exporter ethtool chrony
  log "kernel tuning for UDP ingest"
  install -m 644 deploy/configs/sysctl/90-nexostream.conf /etc/sysctl.d/
  sysctl --system >/dev/null
  install -m 755 bin/nexostream-worker /usr/local/bin/
  install_env worker
  install_unit nexostream-worker
  install -m 644 deploy/configs/nginx/nexostream-origin.conf /etc/nginx/sites-available/nexostream-origin
  ln -sf /etc/nginx/sites-available/nexostream-origin /etc/nginx/sites-enabled/nexostream-origin
  nginx -t && systemctl reload nginx
fi

if [ "$ROLE" = control ] || [ "$ROLE" = all ]; then
  install -m 755 bin/nexostream-api bin/nexostream-controller /usr/local/bin/
  install_env api
  install_env controller
  install_unit nexostream-api
  install_unit nexostream-controller
  if [ -d web-panel/dist ]; then
    log "web panel"
    install -d /usr/share/nexostream/panel
    cp -r web-panel/dist/. /usr/share/nexostream/panel/
  fi
  if [ ! -f /etc/nexostream/jwt.key ]; then
    log "generating /etc/nexostream/jwt.key"
    (umask 027; openssl rand -hex 32 > /etc/nexostream/jwt.key)
    chown root:nexostream /etc/nexostream/jwt.key
  fi
  install -m 644 deploy/configs/nginx/nexostream-panel.conf /etc/nginx/sites-available/nexostream-panel
  echo "nginx panel site installed but not enabled: add the TLS certificate, then link it into sites-enabled."
fi

systemctl daemon-reload
timedatectl set-timezone UTC || true
log "done. Next: edit /etc/nexostream/*.env, then:"
[ "$ROLE" != control ] && echo "  systemctl enable --now nexostream-worker"
[ "$ROLE" != worker ] && echo "  systemctl enable --now nexostream-api nexostream-controller"
