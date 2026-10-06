# 3. Installation

Two ways are described: **Docker Compose** (suggested) and the **binary with systemd**. Both need the
same three things:

1. a PostgreSQL database and its URL;
2. a **master key** (32 random bytes) in a file;
3. the **service URL**: the address phones and Zabbix use to reach Zweep (for example
   `https://zweep.example.local:8080`). It goes in the QR codes and in the media type.

## 3.1 Before you start: names and addresses

Decide now how Zweep will be reached, because the name ends up in the certificates, the QR codes and
the media type:

| Choice | Example | Notes |
|---|---|---|
| DNS name on the internal network (suggested) | `zweep.corp.example.com` | needed for Let's Encrypt or a company CA; survives IP changes |
| IP address | `192.168.10.25` | fine for a lab or a small site; only self-signed or uploaded certificates |
| Public name on the Internet | `zweep.example.com` | only if phones must reach it without VPN (chapter 5.7) |

💡 Use a name even on an internal network: it can be moved to another machine without re-activating
the phones.

## 3.2 The master key

The master key encrypts every secret stored by Zweep: Zabbix API tokens, webhook secrets, TOTP seeds,
certificates and private keys, DNS provider credentials, and the backups.

```bash
mkdir -p secrets
head -c 32 /dev/urandom > secrets/master.key
chmod 600 secrets/master.key
```

The file may contain the 32 raw bytes or their base64 form (`openssl rand -base64 32 > secrets/master.key`).

⚠ **Keep a copy of the master key in a safe place, separate from the backups** (a password manager,
a sealed envelope in the safe, a vault). Without it:

- the backups cannot be restored;
- the secrets in the database cannot be read (every source must get a new secret and token, and
  the certificates must be configured again).

⚠ Never put the master key in the same place as the backups: whoever has both can read every
secret.

## 3.3 Docker Compose (suggested)

The repository has a ready `compose.yaml` (Zweep and PostgreSQL 17) and `.env.example`. Copy them to the
server, for example in `/opt/zweep`:

```
/opt/zweep/
├── compose.yaml
├── .env                    (from .env.example)
├── secrets/
│   ├── master.key          (chmod 600, owner 65532)
│   ├── database-url        (chmod 600, owner 65532)
│   └── postgres-password   (chmod 600)
└── backups/                (owner 65532; copied elsewhere every night)
```

**Before you start**, on the server:

- `docker --version && docker compose version` must both answer: Zweep needs Docker Engine with the
  **Compose plugin** (`docker compose`), not the old `docker-compose` (see the
  [Docker documentation](https://docs.docker.com/engine/install/) to install it).
- If the server is itself a **system container** (LXC and similar) rather than a physical or virtual
  machine, its host must allow nested containers (the option is usually called *nesting*): without it
  Docker fails with permission errors when it starts a container.
- Run the commands as **root**. If you use sudo, open a root shell first (`sudo -i`): in
  `sudo command > file` the redirection is done by your user and fails in a directory of root.

```bash
mkdir -p /opt/zweep && cd /opt/zweep
curl -fsSLO https://raw.githubusercontent.com/N1k0droid/zweep/v1.0.3/compose.yaml
curl -fsSL https://raw.githubusercontent.com/N1k0droid/zweep/v1.0.3/.env.example -o .env

mkdir -p secrets backups
head -c 32 /dev/urandom > secrets/master.key
openssl rand -hex 24 > secrets/postgres-password
printf 'postgres://zweep:%s@db:5432/zweep?sslmode=disable' "$(cat secrets/postgres-password)" > secrets/database-url
chmod 600 secrets/*
chown 65532:65532 backups secrets/master.key secrets/database-url
```

(Or `git clone https://github.com/N1k0droid/zweep.git /opt/zweep`, as in the README: the same files,
updated with `git pull`.)

What the commands do:

| Command | What it creates | Keep it? |
|---|---|---|
| `head -c 32 /dev/urandom > secrets/master.key` | the **master key** (3.2): 32 random bytes that encrypt the secrets in the database and the backups | **yes, a copy offline, away from the backups** |
| `openssl rand -hex 24 > secrets/postgres-password` | the password of the PostgreSQL of the stack | it stays on the server; needed only to open the database by hand |
| `printf 'postgres://…' > secrets/database-url` | the address Zweep uses to reach its database, with that password | no: it can be written again from the password |
| `chmod 600 secrets/*` | makes the three files readable by their owner only | — |
| `chown 65532:65532 …` | gives the two files Zweep reads, and the backup directory, to the user of the container | — |

The container runs as user `nonroot` (uid 65532): it must be able to read its two secret files and write
the backups. `sslmode=disable` is fine because the database is on the private Docker network of the
stack; use `sslmode=verify-full` for a remote database.

Edit `.env`:

| Variable | Meaning |
|---|---|
| `ZWEEP_SERVICE_URLS` | **required**: the address phones and Zabbix use (3.1) |
| `ZWEEP_TRUSTED_PROXIES` | the reverse proxy, if any (chapter 5.3) |
| `ZWEEP_PUBLIC_PORT`, `ZWEEP_ADMIN_PORT` | host ports of the public listener and of the dashboard (always on `127.0.0.1`) |
| `ZWEEP_ADMIN_ALLOWED_IPS` | addresses allowed on the dashboard, when it is published on the LAN (3.6) |
| `ZWEEP_METRICS_LISTEN_HTTP`, `ZWEEP_METRICS_ALLOWED_IPS` | metrics for monitoring (chapter 10.1) |
| `ZWEEP_LISTEN_PLAIN` | plain HTTP listener for Let's Encrypt HTTP-01 (3.3.2) |
| `ZWEEP_BACKUP_HOUR`, `ZWEEP_BACKUP_KEEP` | daily backup in `./backups` |
| `TZ` | time zone of the logs and of the backup hour |
| `ZWEEP_IMAGE` | `ghcr.io/n1k0droid/zweep:1.0.3` or the mirror `docker.io/n1k0droid/zweep:1.0.3` |

Every other option of chapter 4 can be added to the `environment` of the `zweep` service. Secrets are
always files, never variables.

Start it:

```bash
docker compose up -d
docker compose ps                          # zweep and db: "healthy" after a few seconds
curl -s http://127.0.0.1:8080/v1/health    # {"healthy":true}
```

Then try `http://<server>:8080/v1/health` from another PC: phones and Zabbix arrive from the network.
If it answers on the server only, a firewall blocks the port (on the host or on the network). `docker compose logs -f zweep` follows the log.

The service is hardened: read-only file system, no new privileges, no Linux capabilities. Docker
restarts it if it stops (`restart: unless-stopped`).

### 3.3.1 Building the image from the source

The published images are built by the release workflow of the repository for linux/amd64 and
linux/arm64. To build your own:

```bash
git clone https://github.com/N1k0droid/zweep.git && cd zweep
cp /path/to/zweep-1.0.3.apk apk/        # the signed app offered to the phones (release asset)
docker build --build-arg VERSION=1.0.3 -t zweep-server:1.0.3 .
```

Then set `ZWEEP_IMAGE=zweep-server:1.0.3` in `.env`. The build uses base images pinned by digest; the
result has no shell and runs as non-root. `make docker` does the same, and `make sbom` writes a
CycloneDX SBOM of the dependencies in `dist/`.

### 3.3.2 Port 443 and port 80

To serve HTTPS on the standard port, publish the public listener on 443 and tell Zweep its URL, in
`.env`:

```ini
ZWEEP_PUBLIC_PORT=443
ZWEEP_SERVICE_URLS=https://zweep.corp.example.com
ZWEEP_LISTEN_PLAIN=:8090        # only if you need port 80 (chapter 5.6)
```

and, for port 80, uncomment `- "80:8090"` in `compose.yaml`. Inside the container Zweep keeps listening
on unprivileged ports; Docker maps them.

## 3.4 Binary with systemd

Build (Go 1.27, the toolchain named in `go.mod` and used by the image and the CI):

```bash
make build                 # → dist/zweep-server
sudo install -m 755 dist/zweep-server /usr/local/bin/
```

User and directories:

```bash
sudo useradd --system --home /var/lib/zweep --shell /usr/sbin/nologin zweep
sudo install -d -o zweep -g zweep -m 700 /etc/zweep /var/lib/zweep /var/backups/zweep
sudo sh -c 'head -c 32 /dev/urandom > /etc/zweep/master.key'
sudo sh -c 'echo "postgres://zweep:CHANGE-ME@127.0.0.1:5432/zweep" > /etc/zweep/database-url'
sudo chown zweep:zweep /etc/zweep/* && sudo chmod 600 /etc/zweep/*
```

Database (on the PostgreSQL host):

```sql
CREATE ROLE zweep LOGIN PASSWORD 'CHANGE-ME';
CREATE DATABASE zweep OWNER zweep;
```

`/etc/zweep/zweep.env`:

```ini
ZWEEP_DATABASE_URL_FILE=/etc/zweep/database-url
ZWEEP_MASTER_KEY_FILE=/etc/zweep/master.key
ZWEEP_LISTEN_HTTP=:8080
ZWEEP_ADMIN_LISTEN_HTTP=127.0.0.1:8081
ZWEEP_SERVICE_URLS=https://zweep.corp.example.com:8080
ZWEEP_BACKUP_DIR=/var/backups/zweep
ZWEEP_LOG_FORMAT=json
```

`/etc/systemd/system/zweep.service`:

```ini
[Unit]
Description=Zweep for Zabbix
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
User=zweep
Group=zweep
EnvironmentFile=/etc/zweep/zweep.env
ExecStart=/usr/local/bin/zweep-server serve
Restart=always
RestartSec=3
# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=/var/backups/zweep
# Only to bind ports below 1024 (443, 80) without root:
#AmbientCapabilities=CAP_NET_BIND_SERVICE
#CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now zweep
journalctl -u zweep -f
```

## 3.5 First start

At start Zweep:

1. connects to PostgreSQL and creates or migrates its tables (migrations are automatic and run under a
   lock, so several nodes can start together);
2. opens the listeners and logs them (`"msg":"Listening"` with the address and whether TLS is on);
3. if no admin exists yet, prints a **one-time setup token** in the log:

```json
{"level":"WARN","msg":"No admin yet: open the dashboard on the admin port, page /admin/setup, and enter this one-time setup token","component":"dashboard","setup_token":"zws_…","valid_for":"1h0m0s"}
```

Check that it is healthy:

```bash
curl -s http://127.0.0.1:8080/v1/health
docker compose exec zweep zweep-server healthcheck && echo healthy
```

To read only the token:

```bash
docker compose logs zweep | grep -o 'zws_[A-Za-z0-9_-]*' | tail -1
```

The token is valid for one hour and is printed again at every start while no admin exists:
`docker compose restart zweep` gives a new one.

### Creating the first admin

**From the browser.** Open `http://127.0.0.1:8081/admin/` (through an SSH tunnel if the server is
remote: `ssh -L 8081:127.0.0.1:8081 server`). The setup page asks for the setup token, a username and a
password (12 to 1024 characters, different from the username; no other composition rules). The token is
valid 60 minutes and only once; restarting Zweep prints a new one while there is still no admin.

**From the command line** (no browser needed; the password is read from standard input, never from
the arguments, so it does not end up in the shell history or in `ps`):

```bash
docker compose exec -T zweep zweep-server admin bootstrap -username admin < admin-password.txt
# or, with systemd
sudo -u zweep sh -c 'set -a; . /etc/zweep/zweep.env; exec zweep-server admin bootstrap -username admin'
```

Then, in the dashboard:

1. **Users → New user**: create a **second admin** (two-step verification needs one), then **My account →
   Two-step verification (TOTP)** for both (strongly suggested for admins).
2. **Settings → HTTPS**: choose how HTTPS is provided (chapter 5).
3. **Sources → New source**: connect the first Zabbix (chapter 6).
4. **Users → New user**: create the operators (chapter 7) and activate their phones.
5. **Status**: check that everything is green.

### Forgotten password

```bash
docker compose exec -T zweep zweep-server admin reset-password -username admin < new-password.txt
```

This also ends the dashboard sessions of that user. Two-step verification stays as it is.

### The superadmin and two-step verification

The first admin created (setup page or `admin bootstrap`) is the **superadmin**, for good:

- only the superadmin can **reset the two-step verification** of the other accounts (**Users → name → Reset
  two-step verification**), for example when someone loses the phone with the authenticator;
- the superadmin cannot be deleted, disabled or demoted (not even by another admin);
- an admin can turn two-step verification on only while **at least one other active admin** exists:
  with a single admin, a lost authenticator would lock the dashboard.

💡 Create a second admin right after the first one, and keep both with two-step verification.

If the **superadmin** loses the authenticator (nobody else can reset it from the dashboard), use the
command line on the server:

```bash
docker compose exec -T zweep zweep-server admin reset-totp -username admin
```

The account then signs in with the password only and can enable two-step verification again from **My
account**. The command is recorded in the audit trail.

## 3.6 Reaching the dashboard safely

The dashboard (admin port) gives full control of Zweep. Never publish it like the public port:

| Option | How |
|---|---|
| Host loopback + SSH tunnel (default) | `127.0.0.1:8081`, then `ssh -N -L 8081:127.0.0.1:8081 user@server` (below) |
| Management network | `ZWEEP_ADMIN_LISTEN_HTTP=10.0.99.5:8081` (an address on the admin VLAN only) |
| Published on the LAN, with an allow-list | with Docker, a `compose.override.yaml` next to `compose.yaml` (kept by `git pull`) and `ZWEEP_ADMIN_ALLOWED_IPS` in `.env` (below) |
| Reverse proxy with allow-list or SSO | proxy on the admin network → `127.0.0.1:8081` |
| Disabled | `ZWEEP_ADMIN_LISTEN_HTTP=-` (administration only from the command line) |

When HTTPS is on (chapter 5), the dashboard is served over HTTPS too, with the same certificate.

**The SSH tunnel.** On your PC (Linux, macOS, Windows 10 or later):

```bash
ssh -N -L 8081:127.0.0.1:8081 user@server
```

It asks for the password (or uses your key) and then shows nothing: it is working. Leave that window
open and browse `http://127.0.0.1:8081/admin/` **on your PC**; `Ctrl+C` closes the tunnel. Without
`-N` the same command also opens a normal shell on the server: that is expected, and the tunnel lasts
as long as the shell. Any user of the server can open the tunnel; root is not needed.

| What you see | Cause | Fix |
|---|---|---|
| the browser says *connection reset*, and the SSH window prints `channel 3: open failed: administratively prohibited` | the SSH server forbids forwarding (hardened images: `AllowTcpForwarding no`) | in `/etc/ssh/sshd_config` replace that line with `AllowTcpForwarding local` and add `PermitOpen 127.0.0.1:8081` next to it (only this destination is allowed); `sshd -t` to check, then `systemctl restart ssh`. If the file ends with `Match` blocks, edit the line where it is: lines added at the end would belong to the last block |
| `bind: Address already in use` | port 8081 of your PC is taken | use another local port: `-L 18081:127.0.0.1:8081`, then `http://127.0.0.1:18081/admin/` |
| *connection refused* in the browser | the tunnel is closed, or Zweep is not running | check the SSH window and `docker compose ps` |

**Never on the public address.** Behind a reverse proxy, forward only the public port (8080). Check
it from outside: `curl -s -o /dev/null -w '%{http_code}\n' https://<public name>/admin/login` must
print `404`. A `200` means that the proxy points to the admin port: the dashboard is on the Internet.

**Dashboard on the LAN with an allow-list** (Docker). Publish the admin port on the address of the
host as well:

```yaml
# compose.override.yaml
services:
  zweep:
    ports:
      - "192.168.10.25:8081:8081"     # the LAN address of the Docker host
```

and in `.env` allow only the PCs of the administrators:

```ini
# the PCs of the administrators, and the Docker network (keeps the SSH tunnel working, see below)
ZWEEP_ADMIN_ALLOWED_IPS=192.168.10.50,192.168.10.51,172.16.0.0/12
```

then `docker compose up -d`. Any other address gets *403 Forbidden*. Notes:

- **Keep the SSH tunnel**: through the tunnel (`127.0.0.1:8081` of the host) the requests reach the
  container from the gateway of its Docker network, not from the loopback. Without that network in
  the list the tunnel gets *403* too. Docker takes its networks from `172.16.0.0/12` by default; to
  see the one in use: `docker network inspect zweep_zweep | grep Subnet` (the network is named after
  the directory of the stack; `docker network ls` lists them). Normally only
  the Docker host and the containers of the stack come from that network. Check it once: open the
  dashboard from a PC of the LAN and look at the address logged for the request (**Logging**, or
  `docker compose logs zweep`): it must be the address of the PC. If every client shows the address of
  the Docker gateway (rootless Docker, Docker Desktop), the allow-list cannot tell them apart: leave
  the Docker network out and use the tunnel only with a firewall rule, or do not publish the port.
- Behind a reverse proxy listed in `ZWEEP_TRUSTED_PROXIES`, the allow-list applies to the real client
  address.
- A firewall rule does the same outside Zweep. With Docker, published ports bypass the `INPUT` chain
  and `ufw`: use the `DOCKER-USER` chain, e.g.
  `iptables -I DOCKER-USER -p tcp --dport 8081 ! -s 192.168.10.50 -j DROP`.

## 3.7 Upgrading

Zweep migrates the database automatically at start. To upgrade:

1. make a backup (`zweep-server backup`, chapter 10.4);
2. replace the image or the binary;
3. restart; watch the log for errors at start (a failed migration stops the server before it
   listens, so nothing half-migrated is ever served).

With several nodes, upgrade them one at a time: a newer node migrates the schema, older nodes keep
working as long as the release notes do not say otherwise. Downgrades are not supported once a newer
schema has been applied: restore the backup made before the upgrade instead.
