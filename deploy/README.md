# deploy/: signalhub + coturn (+ Caddy in production)

One `docker-compose.yml` serves both stages: **locally** (your computer) and on a **server**, with only `deploy/.env` changing.

| Service | Container | What it does | Local | Production |
|---|---|---|---|---|
| `signalhub` | `<project>-hub` | Signaling (WebSocket) | `ws://127.0.0.1:8090/ws` | behind Caddy |
| `coturn` | `<project>-turn` | STUN and TURN relay | `127.0.0.1:3478` | host network, `:3478` |
| `caddy` | `<project>-caddy` | Automatic TLS (`wss://`) | not started | `:80` and `:443` |

`<project>` is `COMPOSE_PROJECT` (default `signalhub`). It also prefixes the volumes: Caddy keeps its certificates in `<project>_caddy_data`, so keep the same value on a server.

## Local

```bash
make up        # the first time it creates deploy/.env with a random TURN_SECRET,
               # then builds signalhub and starts signalhub + coturn
make health    # ok
make logs      # follow the logs
make down
```

`deploy/.env` is created by `deploy/setup-env.sh` from `.env.example`, with its own `TURN_SECRET` (`openssl rand -hex 32`). An existing `deploy/.env` is never changed.

Clients connect to `ws://127.0.0.1:8090/ws?v=1`. Add your web app's dev server to `ALLOWED_ORIGINS`. If port 3478 is taken on your computer, change `TURN_PORT` and the port in `STUN_URLS`/`TURN_URLS`.

## Production (any server)

Any Linux server with Docker works; 1 GB of RAM is enough (add 1 GB of swap to build the image on it). The server **builds signalhub from its source** with `docker compose`: no image is published to Docker Hub or any other registry.

**Once, on the server:**
1. Docker with the `compose` and `buildx` plugins.
2. A DNS name pointing to the server, for example `signal.example.com`.
3. Open ports: TCP 80 and 443 (signaling and the certificate), TCP and UDP 3478 (STUN/TURN) and the UDP relay range (`TURN_MIN_PORT`-`TURN_MAX_PORT`, e.g. 49300-49400).
**Deploy from your computer** (it sends the last commit, so commit first):

```bash
make deploy SERVER=root@my-server SSH_KEY=~/.ssh/my-key.pem
make deploy-health SERVER=root@my-server SSH_KEY=~/.ssh/my-key.pem   # ok
make deploy-logs SERVER=root@my-server SSH_KEY=~/.ssh/my-key.pem
```

**The first time**, `make deploy` creates `/opt/signalhub/deploy/.env` on the server from `.env.production.example`, with a random `TURN_SECRET` that only the server knows (file mode 600), and stops. Edit that file on the server with your own values (`SIGNAL_DOMAIN`, `ACME_EMAIL`, `ALLOWED_ORIGINS`, `STUN_URLS`, `TURN_URLS`, `TURN_REALM`, `TURN_EXTERNAL_IP`) and run `make deploy` again. While it still has example values (`example.com`), it does not deploy.

`make deploy` copies the repository (`git archive HEAD`) to `REMOTE_DIR` (default `/opt/signalhub`), keeps the server's `deploy/.env` (it is not in git), and runs, on the server:

```bash
cd /opt/signalhub/deploy
docker compose --env-file .env -f docker-compose.yml -f docker-compose.prod.yml up -d --build
```

**Or from a clone on the server:**

```bash
git clone https://github.com/lordbasex/signalhub.git /opt/signalhub
cd /opt/signalhub
make prod-up      # first time: creates deploy/.env with a random TURN_SECRET and stops; set your values
make prod-up      # builds the image and starts signalhub, coturn and Caddy
make update       # later: git pull and restart with the new code
make prod-ps      # containers; make prod-logs, make prod-down
```

On a server always use the `prod-*` targets: `make up` is the local stack (no Caddy, coturn on `127.0.0.1`).

**TLS:** in production Caddy always runs (`docker-compose.prod.yml`). It gets the Let's Encrypt certificate for `SIGNAL_DOMAIN` by itself, keeps it in the `caddy_data` volume (it survives restarts and updates) and **renews it automatically** about 30 days before it expires. Nothing to schedule. The domain must point to the server and ports 80 and 443 must be open.

**coturn on the host network:** publishing a range of relay ports through Docker starts one proxy process per port, too much for a small server, so production uses `network_mode: host`.

### Moving a server that ran an older layout

If the server already ran this stack from another folder, the new folder takes over the same containers and volumes as long as the project name is the same:

1. Copy the old `.env` to `/opt/signalhub/deploy/.env`.
2. Set `COMPOSE_PROJECT` in it to the old project name (the prefix of the old volumes, see `docker volume ls`), so Caddy keeps its certificates.
3. `make deploy`. The containers are recreated with the new names; clients reconnect by themselves.

## coturn hardening

- The secret goes in a config file inside the container (never on the command line, where `ps` would show it). There is no `-n`, so that file is read.
- It never relays into private networks, loopback, link-local or reserved ranges (`169.254.0.0/16` included), in IPv4 and IPv6 (`fc00::/7`, `fe80::/10`, `::ffff:0:0/96`...). Otherwise a public TURN server could be used as a door into the server's own network.
- **Quotas**, so it cannot be used as free bandwidth: `TURN_USER_QUOTA` (sessions per credential, 8), `TURN_TOTAL_QUOTA` (300 in total) and `TURN_MAX_BPS` (**bytes** per second per session: 500000 = 4 Mbps). No TCP relay (`--no-tcp-relay`): WebRTC uses UDP.
- The Docker logs of every service are capped at 3 files of 10 MB.

## signalhub limits

`MAX_CONNS_PER_IP` (20 connections at once per IP, IPv6 by `/64`), `MAX_CONNS` (5000 in total), `MSG_RATE_PER_SEC` (30 messages per second per connection; above it the connection is closed) and `OWNER_RATE_PER_MIN` (30 `register`/`room_open`/`invite_create` per IP). Details in the [main README](../README.md#configuration-environment-variables).
