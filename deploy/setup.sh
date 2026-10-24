#!/usr/bin/env bash
# Разворачивает стенд на чистой Ubuntu 22.04/24.04 или Debian 12. Запуск от root:
#   curl -fsSL https://raw.githubusercontent.com/GogaBalenci/smb-credit-conveyor/main/deploy/setup.sh | bash
#   curl -fsSL .../deploy/setup.sh | SITE_ADDRESS=alfa.example.ru bash   # с доменом и HTTPS
# Повторный запуск обновляет код и пересобирает стенд; данные и секрет сохраняются.
set -euo pipefail

REPO="${REPO:-https://github.com/GogaBalenci/smb-credit-conveyor.git}"
DIR="${DIR:-/opt/credit-conveyor}"
SITE_ADDRESS="${SITE_ADDRESS:-}"
WITH_AI="${WITH_AI:-false}"

log() { printf '\n\033[1;32m==> %s\033[0m\n' "$*"; }
[ "$(id -u)" -eq 0 ] || { echo "Запустите от root (sudo -i)"; exit 1; }

log "Пакеты"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git curl ca-certificates openssl >/dev/null

if ! command -v docker >/dev/null; then
  log "Docker"
  curl -fsSL https://get.docker.com | sh
fi

# Сборка Go и фронтенда на 1–2 ГБ памяти может упасть без подкачки.
if [ "$(free -m | awk '/Mem:/{print $2}')" -lt 3500 ] && ! swapon --show | grep -q .; then
  log "Подкачка 2 ГБ"
  fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile
  grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

# Если Docker Hub недоступен напрямую, подключаем зеркала.
if ! timeout 15 docker pull -q hello-world >/dev/null 2>&1; then
  log "Docker Hub недоступен — подключаю зеркала"
  mkdir -p /etc/docker
  echo '{"registry-mirrors": ["https://dockerhub.timeweb.cloud", "https://mirror.gcr.io"]}' > /etc/docker/daemon.json
  systemctl restart docker
fi

log "Код"
if [ -d "$DIR/.git" ]; then
  git -C "$DIR" pull --ff-only
else
  git clone --depth 1 "$REPO" "$DIR"
fi
cd "$DIR"

if [ ! -f .env ]; then
  log "Конфигурация (.env)"
  cp .env.example .env
  sed -i "s|^JWT_SECRET=.*|JWT_SECRET=$(openssl rand -hex 32)|" .env
fi
if [ -n "$SITE_ADDRESS" ]; then
  sed -i '/^SITE_ADDRESS=/d' .env
  echo "SITE_ADDRESS=$SITE_ADDRESS" >> .env
fi

FILES=(-f docker-compose.yml -f docker-compose.prod.yml)
[ "$WITH_AI" = "true" ] && FILES+=(-f docker-compose.ai.yml)

log "Сборка и запуск (первый раз — 3–7 минут)"
docker compose "${FILES[@]}" up -d --build --remove-orphans

if command -v ufw >/dev/null && ufw status | grep -q active; then
  ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null
fi

ADDR="$(grep '^SITE_ADDRESS=' .env | cut -d= -f2- || true)"
if [ -z "$ADDR" ] || [ "${ADDR#:}" != "$ADDR" ]; then
  ADDR="http://$(curl -fsS4 --max-time 5 https://ifconfig.me || hostname -I | awk '{print $1}')"
else
  ADDR="https://$ADDR"
fi
log "Готово: $ADDR   (Swagger: $ADDR/api/docs)"
docker compose "${FILES[@]}" ps
