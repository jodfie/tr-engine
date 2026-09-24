#!/usr/bin/env sh
# Quick-start tr-engine from your trunk-recorder directory.
# Usage: curl -sL https://raw.githubusercontent.com/LumenPrima/tr-engine/master/install.sh | sh
set -eu

# -- Check we're in a trunk-recorder directory --
if [ ! -f config.json ] && [ ! -f docker-compose.yaml ] && [ ! -f docker-compose.yml ]; then
  echo "Error: This doesn't look like a trunk-recorder directory."
  echo "Run this from the folder that contains trunk-recorder's config.json"
  echo "or docker-compose.yaml."
  exit 1
fi

# -- Check Docker --
if ! command -v docker >/dev/null 2>&1; then
  echo "Error: Docker is required but not installed."
  echo "Install it from https://docs.docker.com/get-docker/"
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "Error: Docker is installed but not running."
  echo "Start Docker and try again."
  exit 1
fi

# -- Check for existing install --
if [ -d tr-engine ]; then
  echo "Error: tr-engine/ directory already exists."
  echo "To reinstall, remove it first:"
  echo "  cd tr-engine && docker compose down -v && cd .. && rm -rf tr-engine"
  exit 1
fi

echo "Setting up tr-engine..."

TR_DIR="$(pwd)"

# -- Create directory and download files --
mkdir -p tr-engine

echo "Downloading files..."
curl -sf https://raw.githubusercontent.com/LumenPrima/tr-engine/master/sample.env \
  -o tr-engine/.env

if [ ! -f tr-engine/.env ]; then
  echo "Error: Failed to download files"
  rm -rf tr-engine
  exit 1
fi

# -- Configure .env with active values --
# Uncomment and set the values needed for this install.
# Everything else stays commented as a reference for the user.
sed -i.bak \
  -e "s|^DATABASE_URL=.*|# DATABASE_URL=  # set automatically by Docker Compose|" \
  -e "s|^MQTT_BROKER_URL=.*|# MQTT_BROKER_URL=tcp://localhost:1883|" \
  -e "s|^# TR_DIR=.*|TR_DIR=/trunk-recorder|" \
  tr-engine/.env
rm -f tr-engine/.env.bak

# -- Generate a random database password (there is no default) --
PG_PASSWORD=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
if grep -q '^POSTGRES_PASSWORD=' tr-engine/.env; then
  sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${PG_PASSWORD}|" tr-engine/.env
  rm -f tr-engine/.env.bak
else
  printf '\nPOSTGRES_PASSWORD=%s\n' "$PG_PASSWORD" >> tr-engine/.env
fi
chmod 600 tr-engine/.env

# -- Generate docker-compose.yml --
cat > tr-engine/docker-compose.yml <<YAML
services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: \${POSTGRES_USER:-trengine}
      POSTGRES_PASSWORD: \${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD in .env}
      POSTGRES_DB: \${POSTGRES_DB:-trengine}
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U \${POSTGRES_USER:-trengine}"]
      interval: 5s
      timeout: 3s
      retries: 5

  tr-engine:
    image: ghcr.io/trunk-reporter/tr-engine:latest
    ports:
      # This machine only by default. To open it to your LAN, set
      # HTTP_BIND_IP in .env (and ADMIN_PASSWORD) — see the install message.
      - "\${HTTP_BIND_IP:-127.0.0.1}:\${HTTP_PORT:-8080}:8080"
    env_file: .env
    environment:
      DATABASE_URL: postgres://\${POSTGRES_USER:-trengine}:\${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD in .env}@postgres:5432/\${POSTGRES_DB:-trengine}?sslmode=disable
      AUDIO_DIR: /data/audio
    volumes:
      - ${TR_DIR}:/trunk-recorder:ro
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  pgdata:
YAML

# -- Detect LAN IP for the success message --
LAN_IP=""
if command -v hostname >/dev/null 2>&1; then
  LAN_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
fi
if [ -z "$LAN_IP" ] && command -v ip >/dev/null 2>&1; then
  LAN_IP=$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
fi
if [ -z "$LAN_IP" ] && command -v ifconfig >/dev/null 2>&1; then
  LAN_IP=$(ifconfig 2>/dev/null | awk '/inet / && !/127.0.0.1/ {print $2; exit}')
fi
HOST="${LAN_IP:-localhost}"

# -- Start --
echo "Pulling images (this may take a minute)..."
cd tr-engine
docker compose pull -q
docker compose up -d

echo ""
echo "========================================="
echo "  tr-engine is running!"
echo "  Open http://localhost:8080 on this machine"
echo "========================================="
echo ""
echo "Call recordings will appear as trunk-recorder captures them."
echo ""
echo "tr-engine only listens on this machine (127.0.0.1) by default."
echo "To open it to other machines on your network, add to tr-engine/.env:"
echo "  ADMIN_PASSWORD=<choose a password>   # enables login (full auth mode)"
echo "  HTTP_BIND_IP=${LAN_IP:-<this machine's LAN IP>}"
echo "then run: docker compose up -d   (and open http://${HOST}:8080)"
echo ""
echo "Configuration:  tr-engine/.env"
echo "  Edit this file to enable MQTT, authentication, transcription, etc."
echo "  Then restart:  docker compose up -d"
echo ""
echo "Useful commands (run from the tr-engine/ directory):"
echo "  docker compose logs -f                  View logs"
echo "  docker compose down                     Stop"
echo "  docker compose up -d                    Start"
echo "  docker compose pull && docker compose up -d   Update to latest version"
echo ""
echo "To remove completely:"
echo "  cd tr-engine && docker compose down -v && cd .. && rm -rf tr-engine"
