# Getting Started — Full Stack (HTTPS + Dashboard)

Production deployment with Caddy (automatic HTTPS), Mosquitto (MQTT broker), tr-dashboard (standalone frontend), and Prometheus metrics — all managed by a single Docker Compose file.

> **Other installation methods:**
> - **[Docker Compose (all-in-one)](./docker.md)** — quick setup with bundled PostgreSQL and MQTT
> - **[Docker with existing MQTT](./docker-external-mqtt.md)** — connect to a broker you already run
> - **[Build from source](./getting-started.md)** — compile everything yourself
> - **[Binary releases](./binary-releases.md)** — download a pre-built binary

## Architecture

```
                    Internet
                       │
            ┌──────────┴──────────┐
            │      Caddy :443     │  ← auto HTTPS (Let's Encrypt / Cloudflare)
            │  reverse proxy      │
            └──┬──────────────┬───┘
               │              │
    api.example.com    dashboard.example.com
               │              │
        ┌──────┴───┐   ┌─────┴──────┐
        │ tr-engine │   │tr-dashboard│
        │  :8080    │   │   :3000    │
        └──┬────┬───┘   └────────────┘
           │    │
    ┌──────┴┐  ┌┴─────────┐
    │Postgres│  │Mosquitto │ ← trunk-recorder publishes here
    │ :5432  │  │  :1883   │
    └────────┘  └──────────┘

    postgres-exporter 127.0.0.1:9187  ← Prometheus scrapes this
```

| Service | Purpose | Exposed Port |
|---------|---------|-------------|
| **caddy** | Reverse proxy, auto HTTPS | `BIND_IP:80`, `BIND_IP:443` |
| **tr-engine** | API + web UI | internal only (behind Caddy) |
| **tr-dashboard** | Standalone dashboard frontend | internal only (behind Caddy) |
| **postgres** | Database | internal only (never published) |
| **mosquitto** | MQTT broker (login required) | `127.0.0.1:1883`, or `MQTT_BIND_IP:1883` when you opt in |
| **postgres-exporter** | Prometheus metrics for PostgreSQL | `127.0.0.1:9187` (unauthenticated, so loopback only) |

## Prerequisites

- **Docker** and **Docker Compose** (v2+)
- A **dedicated IP address** (or host) for Caddy's public ports — set as `BIND_IP` (required; use `0.0.0.0` only if you really want every interface)
- **Two DNS A records** pointing to that IP (for Caddy to issue HTTPS certificates)
- **trunk-recorder** with the [MQTT Status plugin](https://github.com/TrunkRecorder/trunk-recorder-mqtt-status)

## 1. Download

```bash
mkdir tr-engine && cd tr-engine
curl -sO https://raw.githubusercontent.com/trunk-reporter/tr-engine/master/docker-compose.full.yml
mv docker-compose.full.yml docker-compose.yml
curl -sO https://raw.githubusercontent.com/trunk-reporter/tr-engine/master/sample.env
cp sample.env .env
```

## 2. Configure `.env`

Edit `.env` and set these values:

```bash
# REQUIRED — IP address Caddy serves 80/443 on
BIND_IP=203.0.113.10

# REQUIRED — database password; there is no default (openssl rand -hex 24)
POSTGRES_PASSWORD=

# OPTIONAL — only if trunk-recorders on OTHER hosts publish to this broker.
# Mosquitto listens on 127.0.0.1 unless you set this.
# MQTT_BIND_IP=203.0.113.10

# REQUIRED — your domain names (replace with your actual domains)
API_DOMAIN=api.example.com
DASHBOARD_DOMAIN=dashboard.example.com

# REQUIRED — MQTT credentials (trunk-recorder must use these too)
MQTT_USERNAME=trengine
MQTT_PASSWORD=your-mqtt-password-here

# STRONGLY RECOMMENDED — full API auth
ADMIN_PASSWORD=$(openssl rand -base64 32)
# Optional public read token returned by /auth-init.
AUTH_TOKEN=$(openssl rand -base64 32)

# Optional — match your TR plugin's topic prefix
MQTT_TOPICS=trengine/#
```

Generate secure random secrets:

```bash
# Run these and paste the output into .env
openssl rand -base64 32   # → ADMIN_PASSWORD
openssl rand -base64 32   # → AUTH_TOKEN (optional public read token)
openssl rand -hex 24      # → POSTGRES_PASSWORD
openssl rand -hex 16      # → MQTT_PASSWORD
```

> **Important:** For public deployments, set `ADMIN_PASSWORD` so writes require login, role checks, or API keys. `AUTH_TOKEN` is optional in full mode; when set, it acts as a public read token returned by `/auth-init`.

Key variables:

| Variable | Required | Description |
|----------|----------|-------------|
| `BIND_IP` | Yes | Address for Caddy's 80/443; compose refuses to start without it |
| `POSTGRES_PASSWORD` | Yes | Database password; no default, compose refuses to start without it |
| `MQTT_BIND_IP` | No | Publish MQTT on this address for remote trunk-recorders (default `127.0.0.1`) |
| `API_DOMAIN` | Yes | Domain for the tr-engine API + built-in web UI |
| `DASHBOARD_DOMAIN` | Yes | Domain for the tr-dashboard frontend |
| `MQTT_USERNAME` | Yes | MQTT broker credentials (shared with trunk-recorder) |
| `MQTT_PASSWORD` | Yes | MQTT broker password |
| `ADMIN_PASSWORD` | Strongly recommended | Enables full auth, admin login, JWT sessions, and API keys |
| `AUTH_TOKEN` | Optional | Public read token in full mode, or shared token in token mode |
| `WRITE_TOKEN` | No | Deprecated legacy write token |

See [`sample.env`](https://github.com/trunk-reporter/tr-engine/blob/master/sample.env) for all available options. Settings like `TR_DIR`, transcription, file watch, and S3 storage are documented in the [Docker Compose guide](./docker.md).

## 3. Set up Mosquitto

Download the broker config (it sets `allow_anonymous false` and reads `mosquitto/passwd`):

```bash
mkdir -p mosquitto
curl -so mosquitto/mosquitto.conf https://raw.githubusercontent.com/trunk-reporter/tr-engine/master/mosquitto/mosquitto.conf
```

Generate the password file with the same username/password you put in `.env`:

```bash
export MQTT_USERNAME=trengine MQTT_PASSWORD='YOUR_MQTT_PASSWORD'
docker run --rm -e MQTT_USERNAME -e MQTT_PASSWORD -v "$PWD/mosquitto:/mosquitto/config" eclipse-mosquitto:2 \
  sh -c 'mosquitto_passwd -c -b /mosquitto/config/passwd "$MQTT_USERNAME" "$MQTT_PASSWORD" && chown mosquitto:mosquitto /mosquitto/config/passwd'
```

The `chown` matters: Mosquitto runs as its own user and can't read a password file owned by root. Compose refuses to start if `mosquitto/passwd` or `mosquitto/mosquitto.conf` is missing.

## 4. Set up Caddy

Create the config directory:

```bash
mkdir -p caddy
```

Create `caddy/Caddyfile` (replace the domain names with yours):

```caddyfile
api.example.com {
    reverse_proxy tr-engine:8080
}

dashboard.example.com {
    reverse_proxy tr-dashboard:3000
}
```

The dashboard and built-in web pages discover auth mode through `GET /api/v1/auth-init`, so Caddy does not need to inject auth headers. If you are upgrading from an older config that has an `@no_auth` injection block, it is harmless to leave in place during migration, but it is no longer required.

## 5. DNS records

Create two A records pointing to your `BIND_IP`:

| Record | Type | Value | Proxy |
|--------|------|-------|-------|
| `api.example.com` | A | `203.0.113.10` | Cloudflare proxied OK |
| `dashboard.example.com` | A | `203.0.113.10` | Cloudflare proxied OK |

**Cloudflare users:** Set SSL/TLS mode to **Full** (not Full Strict, unless you've configured Caddy with Cloudflare origin certificates). Caddy handles the HTTPS certificate on the origin side.

**Non-Cloudflare users:** Caddy automatically obtains Let's Encrypt certificates. Just make sure ports 80 and 443 are reachable from the internet.

If your MQTT broker needs a DNS name (e.g. for trunk-recorder to connect by hostname):

| Record | Type | Value | Proxy |
|--------|------|-------|-------|
| `mqtt.example.com` | A | `203.0.113.10` | DNS only (gray cloud) |

MQTT is raw TCP — it cannot be proxied through Cloudflare. Mosquitto only listens on that address once you set `MQTT_BIND_IP` in `.env`.

## 6. Start

```bash
docker compose up -d
```

On first run:
- PostgreSQL initializes and tr-engine auto-applies the database schema
- Caddy obtains HTTPS certificates (may take 30-60 seconds)
- Mosquitto starts with authentication enabled
- tr-engine connects to PostgreSQL and Mosquitto

## 7. Verify

```bash
# Check all services are running
docker compose ps

# Check tr-engine logs
docker compose logs tr-engine --tail 20

# Health check (use the API domain or localhost via docker)
curl -s https://api.example.com/api/v1/health | python3 -m json.tool

# Test MQTT login (anonymous clients get "not authorised")
docker compose exec mosquitto mosquitto_pub -h localhost -u trengine -P 'YOUR_MQTT_PASSWORD' -t test -m hello
```

Access your deployment:
- **API + Web UI:** `https://api.example.com`
- **Dashboard:** `https://dashboard.example.com`

## Point trunk-recorder at the broker

Mosquitto only listens on `127.0.0.1` by default. If trunk-recorder runs on the same host, use `tcp://localhost:1883`. If it runs elsewhere, set `MQTT_BIND_IP` in `.env` (for example to `BIND_IP`, or better, a LAN/VPN address), run `docker compose up -d mosquitto`, and use that address below.

In your trunk-recorder `config.json`:

```json
{
  "plugins": [
    {
      "name": "MQTT Status",
      "library": "libmqtt_status_plugin.so",
      "broker": "tcp://YOUR_MQTT_BIND_IP:1883",
      "topic": "trengine/feeds",
      "unit_topic": "trengine/units",
      "console_logs": true,
      "username": "trengine",
      "password": "YOUR_MQTT_PASSWORD"
    }
  ]
}
```

Replace `YOUR_MQTT_BIND_IP` and `YOUR_MQTT_PASSWORD` with your values. Systems and talkgroups auto-populate once trunk-recorder connects.

## Configuration

This guide covers the full-stack-specific setup. For shared configuration topics, see the [Docker Compose guide](./docker.md):

- [TR auto-discovery (TR_DIR)](./docker.md#tr-auto-discovery-tr_dir) — auto-import talkgroup names and watch for new calls
- [File watch mode (WATCH_DIR)](./docker.md#file-watch-mode-watch_dir) — ingest calls from TR's audio directory
- [Filesystem audio (TR_AUDIO_DIR)](./docker.md#filesystem-audio-tr_audio_dir) — serve audio directly from TR's filesystem
- [Transcription (STT)](./docker.md#transcription-stt) — automatic speech-to-text for call recordings
- [Custom web UI files](./docker.md#custom-web-ui-files) — override the built-in web UI

To add volume mounts for these features, edit the `tr-engine` service in your `docker-compose.yml` — the commented-out examples are already there.

## Upgrading

```bash
docker compose pull && docker compose up -d
```

All persistent data lives in bind-mounted directories (`pgdata/`, `audio/`) and named volumes (`mosquitto-data`, `caddy-data`). Check release notes for schema migrations.

> **Security defaults changed.** `BIND_IP` and `POSTGRES_PASSWORD` are now required, Mosquitto and postgres-exporter bind to `127.0.0.1` (set `MQTT_BIND_IP` if remote trunk-recorders publish to this broker), and MQTT requires a login. If your database was created without `POSTGRES_PASSWORD`, its password is still `trengine`: rotate it before switching to the new compose file. See [Security defaults changed](./docker.md#security-defaults-changed).

## Logs

```bash
# All services
docker compose logs -f

# Individual services
docker compose logs -f tr-engine
docker compose logs -f caddy
docker compose logs -f mosquitto
```

## Stopping

```bash
# Stop (data preserved)
docker compose down

# Stop and delete all data (fresh start)
docker compose down -v
```

## Troubleshooting

**Caddy certificate errors:** Check that your DNS records are pointing to `BIND_IP` and that ports 80/443 are reachable. Run `docker compose logs caddy` to see certificate issuance status. If using Cloudflare, ensure SSL mode is set to Full.

**MQTT authentication failures:** Verify that the username/password in your TR `config.json` matches what's in the Mosquitto password file. The password file is generated separately from `.env` — regenerate it if you change `MQTT_PASSWORD`:

```bash
export MQTT_USERNAME=trengine MQTT_PASSWORD='NEW_PASSWORD'
docker run --rm -e MQTT_USERNAME -e MQTT_PASSWORD -v "$PWD/mosquitto:/mosquitto/config" eclipse-mosquitto:2 \
  sh -c 'mosquitto_passwd -c -b /mosquitto/config/passwd "$MQTT_USERNAME" "$MQTT_PASSWORD" && chown mosquitto:mosquitto /mosquitto/config/passwd'
docker compose restart mosquitto
```

If Mosquitto logs `Unable to open pwfile`, the file is missing or not owned by the `mosquitto` user; rerun the command above.

**Write operations failing (403):** Log in with an editor/admin user, or use an API key with write access. For upload plugins, create a `tre_...` API key and use it as the plugin API key. The legacy `WRITE_TOKEN` path still works during the deprecation period.

**Dashboard can't reach the API:** The tr-dashboard container connects to tr-engine via the Docker network (`http://tr-engine:8080`). Check that `TR_AUTH_TOKEN` in the compose file matches your `AUTH_TOKEN`.

**Web UI prompts for token:** In token mode, enter `AUTH_TOKEN`. In full mode, log in with your admin user. If you intended open mode, make sure both `AUTH_TOKEN` and `ADMIN_PASSWORD` are unset and the service has been restarted with `docker compose up -d`.
