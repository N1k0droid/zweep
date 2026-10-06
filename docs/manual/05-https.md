# 5. HTTPS and certificates

Everything that travels between Zabbix, the phones and Zweep — alarms, host names, acknowledgements,
the activation of the phones — should be encrypted, **also on an internal network**. Zweep can
provide HTTPS in five ways and switches between them **without restart**, on every node, from
**Dashboard → Settings → HTTPS** (admins only).

## 5.1 Which mode should I choose?

```
Is there already a reverse proxy (nginx, Traefik, Caddy, HAProxy, F5…) for your services?
 └─ yes ──────────────────────────────────────────────► A. Reverse proxy (mode "No HTTPS here")
 └─ no
     Does the company have a CA that speaks ACME (step-ca, Smallstep, EJBCA, Vault PKI, Windows + ACME)?
      └─ yes ─────────────────────────────────────────► B. Company CA, automatic (ACME)
      └─ no
          Do you have a DNS name in a public domain, with DNS at Cloudflare, Route 53, OVH,
          or any provider (through acme-dns)?
           └─ yes ────────────────────────────────────► C. Let's Encrypt with dns-01
          Is the certificate bought, or issued by hand by the company CA (.cer/.key, .pfx)?
           └─ yes ────────────────────────────────────► D. Upload
          Otherwise (lab, small site, IP address only)
           └─────────────────────────────────────────► E. Self-signed with the key in the QR code
```

| Mode | Renewal | Phones trust it because… | Zabbix trusts it because… | Good for |
|---|---|---|---|---|
| A. Reverse proxy | proxy's job | proxy's certificate | proxy's certificate | companies with a standard proxy |
| B. Company CA (ACME) | automatic | the company CA is installed on them, or its key is in the QR code | the company CA is in the trust store of the Zabbix server | companies with an internal PKI |
| C. Let's Encrypt | automatic | public CA | public CA | anyone with a public domain |
| D. Upload | **by hand** (warning 30 days before) | public CA or company CA installed | same | bought certificates, wildcard |
| E. Self-signed | automatic, same key | the key in the QR code (pinning) | **does not**: see 5.8 | labs, IP addresses, small sites |

💡 Certificate lifetimes are getting shorter (public certificates will last at most 47 days from 2029).
Modes A, B, C and E renew themselves; with D somebody must remember to upload the new certificate.
Prefer an automatic mode when you can.

## 5.2 How HTTPS works in Zweep

- The public port (8080, or 443) and the admin port (dashboard) serve **HTTPS and plain HTTP on the
  same port**: Zweep looks at the first byte of each connection.
- When HTTPS is on, plain HTTP requests are **redirected** to HTTPS (`308` for pages and `GET`
  requests) or **refused** (`426 Upgrade Required` for the webhook and other `POST`s: a client that
  posts secrets in clear text must be fixed, not redirected). `GET /v1/health` stays reachable in
  plain HTTP for container health checks.
- The URLs given to the phones (QR codes, configuration) become `https://` as soon as HTTPS is on.
- Certificates and keys are stored **encrypted with the master key** in the database, shared by every
  node. Renewals are coordinated: only one node at a time talks to the CA.
- Every change, issuance and failure is in the audit trail (`tls.cert_obtained`, `tls.cert_failed`)
  and on the Status page.

The **Ports and access** card holds the options that apply to all modes:

| Option | Default | Meaning |
|---|---|---|
| Keep plain HTTP too | off | During a migration, apps and Zabbix on `http://` keep working while you move them to `https://`. Turn it off when done. Never allowed with "Reachable from the Internet". |
| Reachable from the Internet | off | Turn it on **only** if the server is published on the Internet. HTTPS becomes mandatory, plain HTTP is always refused, and the Let's Encrypt port-based methods become available. |
| Port 80: redirect to HTTPS | off | Answer on port 80 with a redirect to HTTPS (needs `ZWEEP_LISTEN_PLAIN`). |
| Addresses allowed on port 80 | empty (all) | IPs or CIDRs allowed to use the redirect on port 80. The ACME http-01 path is always answered (only with its token). |
| Fingerprint in the enrollment QR code | Automatic | Which key the phones pin (5.9). |

## 5.3 A. Behind a reverse proxy

Choose **No HTTPS here (reverse proxy)**. The proxy terminates TLS and forwards to Zweep in plain HTTP.

Requirements for the proxy:

1. forward **WebSocket** upgrades on `/v1/stream` and do not close idle connections before 2–3 minutes;
2. do not buffer responses on `/v1/stream`;
3. set `X-Forwarded-For` and `X-Forwarded-Proto`;
4. list the proxy in `ZWEEP_TRUSTED_PROXIES`;
5. set `ZWEEP_SERVICE_URLS` to the **public** HTTPS address;
6. forward to the **public port** of Zweep (8080), never to the admin port (8081).

With Docker, in `.env`:

```ini
ZWEEP_SERVICE_URLS=https://zweep.example.com:8443     # the address of the proxy, as phones and Zabbix see it
ZWEEP_TRUSTED_PROXIES=192.168.10.2                    # the address of the proxy, as Zweep sees it
```

then `docker compose up -d`, and in the dashboard **Settings → HTTPS → No HTTPS here (reverse proxy)**.
Check from outside the network:

```bash
curl -s https://zweep.example.com:8443/v1/health                                # {"healthy":true}
curl -s -o /dev/null -w '%{http_code}\n' https://zweep.example.com:8443/admin/login   # 404
```

Proxies configured from a web interface (appliances, NAS, firewalls) often forward WebSocket only
when it is enabled explicitly for the rule: without it the app activates but stays "not connected".

### nginx

```nginx
map $http_upgrade $connection_upgrade { default upgrade; '' close; }

server {
    listen 443 ssl;
    http2 off;                      # WebSocket over HTTP/1.1
    server_name zweep.corp.example.com;
    ssl_certificate     /etc/nginx/tls/zweep.crt;
    ssl_certificate_key /etc/nginx/tls/zweep.key;

    client_max_body_size 1m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 300s;    # longer than ZWEEP_KEEPALIVE (60 s)
        proxy_send_timeout 300s;
        proxy_buffering off;
    }
}
```

### Caddy

```caddy
zweep.corp.example.com {
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
    }
}
```

Caddy handles WebSocket and `X-Forwarded-*` by itself, and obtains its certificate automatically.

### Traefik (Docker labels)

```yaml
    labels:
      - traefik.enable=true
      - traefik.http.routers.zweep.rule=Host(`zweep.corp.example.com`)
      - traefik.http.routers.zweep.entrypoints=websecure
      - traefik.http.routers.zweep.tls=true
      - traefik.http.services.zweep.loadbalancer.server.port=8080
```

### HAProxy

```haproxy
frontend https
    bind :443 ssl crt /etc/haproxy/zweep.pem
    http-request set-header X-Forwarded-Proto https
    option forwardfor
    default_backend zweep

backend zweep
    timeout tunnel 10m              # WebSocket
    server z1 127.0.0.1:8080 check
```

⚠ Do **not** proxy the admin port together with the public one. If the dashboard must go through a
proxy, use a separate virtual host reachable only from the admin network, with an allow-list.

## 5.4 B. Company CA with ACME, and C. Let's Encrypt

Choose **Automatic: Let's Encrypt or company CA (ACME)**. Zweep obtains the certificate, renews it
when a third of its validity is left (or earlier if the CA asks, through ACME Renewal Information), and
serves a temporary self-signed certificate until the first one is issued.

Fields:

| Field | Description |
|---|---|
| Certificate authority | *Let's Encrypt*, *Let's Encrypt staging* (for tests: not trusted), or *Company CA (ACME directory)*. |
| ACME directory URL | Company CA only, e.g. `https://ca.corp.example.com/acme/acme/directory`. |
| Root of the CA (PEM) | Company CA only, if Zweep does not already trust it: paste the root certificate. |
| External Account Binding | Only if the CA requires it (key ID and HMAC key given by the CA). Stored encrypted. |
| Domain names | One per line. For Let's Encrypt, public DNS names only (no IP addresses, no `.local`). |
| E-mail for the CA | Optional: the CA writes here about problems with the certificate. |
| Validation method | see below |

### Validation methods

| Method | How the CA checks you control the name | Server on the Internet? | Notes |
|---|---|---|---|
| **dns-01** | Zweep writes a temporary TXT record `_acme-challenge.<name>` through the API of your DNS provider | **no**: works for internal servers | suggested for Let's Encrypt |
| http-01 | the CA connects to port 80 of the name | yes (Let's Encrypt) | needs `ZWEEP_LISTEN_PLAIN=:80` |
| tls-alpn-01 | the CA connects to port 443 of the name | yes (Let's Encrypt) | the public port must be 443 |

With **Let's Encrypt**, http-01 and tls-alpn-01 are accepted only when **Reachable from the Internet**
is on, because Let's Encrypt validates from the Internet. A **company CA** usually reaches internal
servers, so all three methods are available.

### DNS providers for dns-01

| Provider | What to create | Fields |
|---|---|---|
| **Cloudflare** | an API token with *Zone → DNS → Edit* on the zone only (not the global API key) | API token |
| **AWS Route 53** | an IAM user with `route53:ListHostedZones*`, `route53:ListResourceRecordSets`, `route53:ChangeResourceRecordSets`, `route53:GetChange` | access key ID, secret access key, region and hosted zone ID (optional) |
| **OVH** | an application at `https://eu.api.ovh.com/createToken/` with GET/POST/PUT/DELETE on `/domain/zone/*` | endpoint (`ovh-eu`), application key, application secret, consumer key |
| **acme-dns** | for **any other provider**, including those without an API (Register.it, Aruba…): see below | server URL, username, password, subdomain |

All credentials are stored encrypted with the master key and never shown again (`********`).

💡 Give the DNS credentials the **least privilege** possible: only the zone of Zweep, only DNS records.
If you can, delegate a dedicated subzone (`zweep.corp.example.com`) and give rights only on it.

#### acme-dns: providers without an API

[acme-dns](https://github.com/joohoi/acme-dns) is a tiny DNS server that holds only ACME TXT records.
You register once, add **one CNAME** at your provider by hand, and from then on renewals need nothing:

1. Register at an acme-dns server (your own, or a public one you trust):

   ```bash
   curl -s -X POST https://auth.acme-dns.example.org/register
   # → {"username":"…","password":"…","fulldomain":"d420c923-….auth.acme-dns.example.org","subdomain":"d420c923-…"}
   ```

2. At your DNS provider (e.g. the Register.it or Aruba panel) create once:

   ```
   _acme-challenge.zweep.corp.example.com.  CNAME  d420c923-….auth.acme-dns.example.org.
   ```

3. In Zweep choose *acme-dns* and enter server URL, username, password and subdomain.

### Example: Let's Encrypt for an internal server, DNS on Cloudflare

Situation: Zweep at `192.168.10.25`, internal only; the company domain `example.com` is on Cloudflare.

1. Internal DNS: `zweep.corp.example.com → 192.168.10.25` (the name does not need to exist in the public
   DNS: Let's Encrypt only checks the TXT record).
2. Cloudflare: *My Profile → API Tokens → Create Token → Edit zone DNS*, zone `example.com`.
3. `ZWEEP_SERVICE_URLS=https://zweep.corp.example.com:8080` and restart.
4. Dashboard → Settings → HTTPS → Automatic → *Let's Encrypt staging* first, names
   `zweep.corp.example.com`, method *dns-01*, provider *Cloudflare*, token → **Apply**.
5. After a minute the Current certificate card shows the staging certificate. If it fails, the **Last
   error** says why (wrong token, zone not found, propagation…).
6. Switch to *Let's Encrypt* → **Apply**.

💡 Always try with **staging** first: Let's Encrypt limits failed attempts on the production service
(5 failures per hour per name), staging does not.

### Example: company CA with step-ca

```
ACME directory URL:  https://ca.corp.example.com/acme/acme/directory
Root of the CA:      (paste root_ca.crt if Zweep does not already trust it)
Domain names:        zweep.corp.example.com
Validation method:   http-01   (the CA reaches internal servers; needs ZWEEP_LISTEN_PLAIN=:80)
                     or tls-alpn-01 (public port 443)
                     or dns-01 (if the internal DNS has an API)
QR fingerprint:      Automatic → the key of the CA (phones that do not have the company CA
                     installed trust it through the QR code)
```

Company CAs often issue short certificates (24 hours with step-ca by default): Zweep renews them
automatically, and the phones keep working because they pin the CA key, not the certificate.

## 5.5 D. Upload a certificate

Choose **Uploaded certificate** and provide one of:

- certificate (`.cer`, `.crt`, `.pem`, PEM or DER) **and** private key (`.key`, PEM);
- a `.pfx` / `.p12` file **and** its password.

Zweep checks before accepting it, and refuses with a clear message when:

| Message | Cause |
|---|---|
| no certificate found | the file is not a certificate (or the key was uploaded in the certificate field) |
| no private key | missing key, or the `.key` is encrypted: export it without a passphrase |
| the key does not match the certificate | the wrong `.key` |
| expired / not yet valid | check the dates |
| wrong password, or damaged file | the `.pfx` password is wrong, or the file is not a valid PKCS#12 |

and warns (accepting it anyway) when:

- the **chain is missing**: the file contains only the server certificate, without the intermediate
  certificates. Phones and Zabbix may then refuse it. Put the server certificate first and then the
  intermediates in the same `.pem`/`.crt`, or upload the `.pfx` with the full chain;
- it **expires soon** (less than 30 days).

Useful commands:

```bash
# Look inside a certificate
openssl x509 -in zweep.crt -noout -subject -issuer -dates -ext subjectAltName

# Build a full chain: server certificate first, then intermediates
cat zweep.crt intermediate.crt > zweep-fullchain.crt

# Remove the passphrase from a key
openssl pkey -in zweep-encrypted.key -out zweep.key

# From .pfx to PEM (for checking)
openssl pkcs12 -in zweep.pfx -nokeys -out chain.pem
openssl pkcs12 -in zweep.pfx -nocerts -nodes -out zweep.key
```

Windows CA (AD CS): request a *Web Server* certificate with the DNS name in the *Subject Alternative
Name*, export it **with the private key** as `.pfx` including all certificates of the path, and upload
the `.pfx`.

⚠ An uploaded certificate is **not renewed**. Zweep warns 30 days before expiry on the Status page and
in the HTTPS page, and in the last 14 days the Status page shows a warning. Put a reminder in your
calendar too.

## 5.6 Port 80

Port 80 is **off by default**. Turn it on (`ZWEEP_LISTEN_PLAIN=:80`, or `:8090` mapped to 80 in
Docker) only for:

- the **ACME http-01** challenge;
- a **redirect** to HTTPS for people who type `http://` in a browser (option *Port 80: redirect to
  HTTPS*), limited if you like to some addresses (*Addresses allowed on port 80*, e.g.
  `10.0.0.0/8`).

On port 80 Zweep serves nothing else: only the challenge token and, if enabled, the redirect (to the
names of the server only, never to an arbitrary host).

## 5.7 Publishing on the Internet

The default and suggested setup keeps Zweep internal and phones connected through Wi-Fi or VPN. If the
phones must reach it from anywhere without VPN:

1. use a valid certificate (Let's Encrypt or upload) — not self-signed;
2. turn on **Reachable from the Internet**: plain HTTP is refused everywhere, even if *Keep plain
   HTTP too* was on;
3. publish **only the public port** (443) — never the admin port;
4. restrict the webhook to the address of the Zabbix server: **Sources → source → Allowed webhook addresses**;
5. keep the defaults of the built-in limits (chapter 4.2), monitor `zweep_auth_failures_total`,
   `zweep_banned_ips` and `zweep_rate_limited_total` (chapter 10.1);
6. consider a reverse proxy or WAF in front of Zweep if your policy requires one.

## 5.8 Zabbix and HTTPS

The Zweep media type runs on the **Zabbix server**, which must trust the certificate of Zweep. With
a self-signed certificate it does not, and every alarm fails with a TLS error (and the escalation
moves on). Options:

| Certificate of Zweep | What to do for Zabbix |
|---|---|
| Let's Encrypt / public CA | nothing |
| Company CA | install the root of the company CA in the trust store of the Zabbix server host (below) |
| Self-signed | (a) keep *Keep plain HTTP too* on and the media type on `http://` **only on a trusted network segment**; or (b) put a reverse proxy with a trusted certificate in front for Zabbix; or (c) use a company CA instead |

Installing a CA on the Zabbix server host:

```bash
# Debian / Ubuntu
sudo cp company-root.crt /usr/local/share/ca-certificates/company-root.crt
sudo update-ca-certificates
sudo systemctl restart zabbix-server

# RHEL / Rocky / Alma
sudo cp company-root.crt /etc/pki/ca-trust/source/anchors/
sudo update-ca-trust
sudo systemctl restart zabbix-server
```

For the official Zabbix containers, mount the CA bundle (e.g. `/etc/ssl/certs/ca-certificates.crt`) of a
host that trusts the company CA into the container at the same path.

After the change, update **server_url** of the media type to `https://…` and remove
`allow_plaintext=true` (chapter 6.3).

## 5.9 Phones and certificate pinning

The phone accepts the certificate of Zweep in two ways:

1. **the system trust store**: public CAs, or a company CA installed on the phone (often by an MDM);
2. **the key fingerprint** (SPKI SHA-256) carried by the QR code (or typed in the *Key fingerprint*
   field when adding a server by hand). With a fingerprint, the phone accepts **only** certificates
   whose chain ends in that key — stronger than a CA, because no other certificate is accepted.

*Fingerprint in the enrollment QR code*:

| Choice | Pinned key | When |
|---|---|---|
| Automatic (default) | self-signed → the certificate key; company ACME CA → the CA key; Let's Encrypt and uploaded → none | almost always |
| None | — | the phones trust the CA (public or installed) |
| Certificate key | the leaf key | self-signed |
| Key of the CA | the topmost certificate of the chain | company CA not installed on the phones |

Why renewals do not break the phones:

- **self-signed**: Zweep renews the certificate (397 days, renewed 30 days before expiry or when the
  names change) **with the same key**, so the fingerprint never changes;
- **company CA**: the pinned key is the CA's, which does not change at each renewal.

⚠ If you switch mode in a way that changes the pinned key (for example from self-signed to a company
CA without installing it on the phones), the phones already activated will refuse the new certificate
and must be activated again with a new QR code. Plan such changes, and keep *Keep plain HTTP too* off
so you notice at once.

The app shows how each server is protected in **Settings → Servers**: 🔒 *Encrypted (certificate)*,
🔒 *Encrypted (pinned key)*, or ⚠ *Not encrypted*.

## 5.10 Migrating a running installation from HTTP to HTTPS

1. Configure HTTPS (any mode) with **Keep plain HTTP too** on. Existing phones and the media type keep
   working on `http://`.
2. Update `ZWEEP_SERVICE_URLS` to `https://…` if needed. New QR codes now carry `https://` and the
   fingerprint.
3. Make the Zabbix server trust the certificate (5.8) and change **server_url** of the media type to
   `https://…`. Send a test (Zabbix: *Media types → Zweep → Test*).
4. Phones: from **Users → operator → Activation code**, give each operator a new QR code; in the app
   log out of the old server entry and scan it. Then revoke the old device from the dashboard.
5. Check **Devices**: no device should be connected over plain HTTP any more (the log of each request
   has `"tls":true|false`).
6. Turn **Keep plain HTTP too** off.

## 5.11 Problems with certificates

| Symptom | Likely cause | Fix |
|---|---|---|
| Status page: "HTTPS certificate: …" error | the CA refused, or the DNS provider failed | read the full message; try staging; check the token rights and the zone |
| "The certificate authority has not issued the certificate yet" | first issuance in progress or failing | meanwhile the self-signed certificate is served; check *Last error* |
| "The certificate does not cover these addresses given to the app" | `ZWEEP_SERVICE_URLS` contains names not in the certificate | add the names to the certificate, or fix the service URLs |
| Zabbix: `Zweep delivery failed: … SSL certificate problem` | Zabbix does not trust the certificate | 5.8 |
| App: "Connection failed… its certificate is not trusted by this device" | self-signed or company CA without fingerprint | activate with the QR code (it carries the fingerprint) or install the CA |
| App stops connecting after a change of HTTPS mode | the pinned key changed | new activation (5.9) |
| `curl` works, browser says "not secure" | self-signed: expected | use a CA-issued certificate for the dashboard, or accept it for that host |
| Let's Encrypt: "too many failed authorizations" | rate limits of production | wait one hour; use staging while testing |
