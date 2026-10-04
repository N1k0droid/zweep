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

```bash
sudo mkdir -p /opt/zweep && cd /opt/zweep
curl -fsSLO https://raw.githubusercontent.com/N1k0droid/zweep/v1.0.0/compose.yaml
curl -fsSL https://raw.githubusercontent.com/N1k0droid/zweep/v1.0.0/.env.example -o .env

mkdir -p secrets backups
head -c 32 /dev/urandom > secrets/master.key
openssl rand -hex 24 > secrets/postgres-password
printf 'postgres://zweep:%s@db:5432/zweep?sslmode=disable' "$(cat secrets/postgres-password)" > secrets/database-url
chmod 600 secrets/*
sudo chown 65532:65532 backups secrets/master.key secrets/database-url
```

The container runs as user `nonroot` (uid 65532): it must be able to read its two secret files and write
the backups. `sslmode=disable` is fine because the database is on the private Docker network of the
stack; use `sslmode=verify-full` for a remote database.

Edit `.env`:

| Variable | Meaning |
|---|---|
| `ZWEEP_SERVICE_URLS` | **required**: the address phones and Zabbix use (3.1) |
| `ZWEEP_TRUSTED_PROXIES` | the reverse proxy, if any (chapter 5.3) |
| `ZWEEP_PUBLIC_PORT`, `ZWEEP_ADMIN_PORT` | host ports of the public listener and of the dashboard (always on `127.0.0.1`) |
| `ZWEEP_LISTEN_PLAIN` | plain HTTP listener for Let's Encrypt HTTP-01 (3.3.2) |
| `ZWEEP_BACKUP_HOUR`, `ZWEEP_BACKUP_KEEP` | daily backup in `./backups` |
| `TZ` | time zone of the logs and of the backup hour |
| `ZWEEP_IMAGE` | `ghcr.io/n1k0droid/zweep:1.0.0` or the mirror `docker.io/n1k0droid/zweep:1.0.0` |

Every other option of chapter 4 can be added to the `environment` of the `zweep` service. Secrets are
always files, never variables.

Start it:

```bash
docker compose up -d
docker compose logs -f zweep
```

The service is hardened: read-only file system, no new privileges, no Linux capabilities.

### 3.3.1 Building the image from the source

The published images are built by the release workflow of the repository for linux/amd64 and
linux/arm64. To build your own:

```bash
git clone https://github.com/N1k0droid/zweep.git && cd zweep
cp /path/to/zweep-1.0.0.apk apk/        # the signed app offered to the phones (release asset)
docker build --build-arg VERSION=1.0.0 -t zweep-server:1.0.0 .
```

Then set `ZWEEP_IMAGE=zweep-server:1.0.0` in `.env`. The build uses base images pinned by digest; the
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

- only he can **reset the two-step verification** of the other accounts (**Users → name → Reset
  two-step verification**), for example when someone loses the phone with the authenticator;
- he cannot be deleted, disabled or demoted (not even by another admin);
- an admin can turn two-step verification on only while **at least one other active admin** exists:
  with a single admin, a lost authenticator would lock the dashboard.

💡 Create a second admin right after the first one, and keep both with two-step verification.

If the **superadmin** loses his authenticator (nobody else can reset it from the dashboard), use the
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
| Host loopback + SSH tunnel (default) | `127.0.0.1:8081`, then `ssh -L 8081:127.0.0.1:8081 server` |
| Management network | `ZWEEP_ADMIN_LISTEN_HTTP=10.0.99.5:8081` (an address on the admin VLAN only) |
| Reverse proxy with allow-list or SSO | proxy on the admin network → `127.0.0.1:8081` |
| Disabled | `ZWEEP_ADMIN_LISTEN_HTTP=-` (administration only from the command line) |

When HTTPS is on (chapter 5), the dashboard is served over HTTPS too, with the same certificate.

## 3.7 Upgrading

Zweep migrates the database automatically at start. To upgrade:

1. make a backup (`zweep-server backup`, chapter 10.4);
2. replace the image or the binary;
3. restart; watch the log for errors at start (a failed migration stops the server before it
   listens, so nothing half-migrated is ever served).

With several nodes, upgrade them one at a time: a newer node migrates the schema, older nodes keep
working as long as the release notes do not say otherwise. Downgrades are not supported once a newer
schema has been applied: restore the backup made before the upgrade instead.
