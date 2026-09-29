# Public HTTPS access via Cloudflare Tunnel + Access (Design B)

Puts `https://<hostname>` in front of the UI from anywhere in the world,
with **no inbound ports** on the phone or router:

```
browser ──> Cloudflare edge (TLS + Access auth) <── outbound tunnel ── cloudflared (phone) ──> http://127.0.0.1:8099 (nvrd)
```

- **cloudflared** runs on the phone as a supervised `nvrctl` component
  (`tunnel`), watchdog-restarted and started at boot like everything else.
  The connection to Cloudflare is outbound-only; nvrd keeps binding
  `0.0.0.0:8099` for the LAN exactly as before.
- **Cloudflare Access** (Zero Trust, free ≤50 users) is the Layer-1 gate:
  only identities you approve even reach nvrd's login page. The API token
  stays as the inner layer — see "Auth model" below.
- Live video: HLS is proxied through the tunnel; WebRTC signaling goes
  through nvrd's WHEP proxy while media flows directly over UDP (falls
  back to HLS where UDP can't traverse).

## Prerequisites

- A domain whose DNS is on your Cloudflare account (free plan is fine).
- One Cloudflare login for `tunnel login` (one-time).

## Bring-up — Method A: dashboard-managed tunnel (recommended, no login)

Nothing to authenticate on the phone; the tunnel and its DNS are managed
in the Zero Trust dashboard, the phone only holds a connector token.

1. `scripts/fetch-cloudflared.sh && scripts/deploy.sh` (pushes cloudflared).
2. Zero Trust dashboard → **Networks → Tunnels → Create tunnel** →
   connector type *cloudflared*, name it `pocketnvr` → on the install
   screen **copy the token** (the long `eyJ…` string).
3. `scripts/cloudflare-token.sh 'eyJ…'` — installs the token at
   `/data/nvr/cloudflare/token.txt` (600) and starts the supervised
   `tunnel` component (watchdog + boot start included).
4. Back in the dashboard, same tunnel → **Public hostname** tab → add:
   subdomain `nvr`, your domain, service `HTTP://localhost:8099`.
   This creates the DNS record; nothing to run on the phone.
5. Finish Layer 1 (Access) below, then set
   `notifications.base_url: https://nvr.yourdomain.com`.

`nvrctl logs tunnel` for logs; `nvrctl restart tunnel` to pick up a
re-installed token. Ingress (hostname/service) is edited in the
dashboard — there is no tunnel.yml in this mode.

## Bring-up — Method B: CLI-managed tunnel (setup.sh)

Self-managed ingress via `/data/nvr/cloudflare/tunnel.yml`:

```sh
# 1. Mac: fetch the pinned arm64 binary and deploy it with the rest
scripts/fetch-cloudflared.sh
scripts/deploy.sh

# 2. Phone (over adb): provision tunnel + DNS + ingress config
adb shell su -c "sh /data/nvr/cloudflare/setup.sh nvr.example.com"
#    `tunnel login` prints a URL — open it in any browser, authorize,
#    pick the zone for your hostname; the script continues on its own.
#    Re-run with FORCE=1 to overwrite an existing DNS record.
#    If the login stalls: run `cloudflared tunnel login` on the Mac
#    (third_party/cloudflared/current/darwin_arm64/cloudflared) and
#    `adb push ~/.cloudflared/cert.pem /data/nvr/cloudflare/cert.pem`,
#    then rerun setup.sh — or just use Method A.
```

From here `nvrctl watch` keeps the tunnel alive (backoff restarts) and
`service.sh` starts it on every boot. `nvrctl logs tunnel` for logs,
`nvrctl restart tunnel` after edits to `/data/nvr/cloudflare/tunnel.yml`.

## Layer 1 — Cloudflare Access (the part configured in the dashboard)

One Zero Trust → Access → Applications → **Add → Self-hosted** app:

1. **Application domain:** your hostname (`nvr.example.com`).
2. **Session duration:** deliberate re-auth cadence, e.g. 24h (max 1
   month; shorter = revoked people are locked out sooner).
3. **Next: policy** — Action `Allow`, Include:
   - *Emails* → the addresses you approve (login = one-time PIN mailed to
     them), or
   - *Login with Google* → specific Google accounts (or a Google group,
     so approval = group membership).
   Approval = adding an identity to this policy; revocation = removing it.
4. **Login settings — expired sessions must return 401, not redirect:**
   configure the app so expired-session requests get an HTTP 401 instead
   of the `cloudflareaccess.com` login redirect. The PWA's fetch/SSE/HLS
   calls all rely on this to fail cleanly into the existing login gate
   (see [session management docs](https://developers.cloudflare.com/cloudflare-one/access-controls/access-settings/session-management/)).
5. Optional **session revocation** (Zero Trust → Settings → Authentication)
   so removing someone mid-session cuts them off immediately.

Optional hardening, same dashboard:

- **Geo-fence:** Security → WAF → custom rule: allow only the country/
  countries your users live in; block the rest. Free plan allows 5 rules.
- **Audit:** Access → Logs shows who reached the NVR and when.

## Wire the deep links + verify

- `config.yaml` → `notifications.base_url: https://nvr.example.com`
  (or via the UI's settings page), then reload — push deep links now open
  the HTTPS origin and are useless to anyone Access didn't let in.
- Check the gate before ever sharing the URL:

```sh
curl -si https://nvr.example.com/api/health | head -1
# 302/redirect to <team>.cloudflareaccess.com  -> Access gate working
# after logging in once in a browser:          -> 200 OK
```

- Open the site in a browser, log in with an approved identity, enter the
  API token at the login gate, then install the PWA from that URL.

## Auth model (why two layers)

| Layer | What it checks | Revocation |
|---|---|---|
| Cloudflare Access | *who you are* (approved identity at the edge) | remove from policy (instant with session revocation) |
| nvrd API token | *shared secret* at the app (survives a Cloudflare misconfiguration) | rotate in `secrets.yaml` + reload |

Notes: the token is a single shared secret (`secrets.yaml: api_token`) —
if you ever remove a person, rotate it too, since they saw it once.
Deep links embed the token (`?token=`) but sit behind Access, so a leaked
link is dead weight to outsiders. Future option: nvrd validates the
Access JWT (`Cf-Access-Jwt-Assertion`) for true per-user identity at the
origin — must be signature validation, never the plain email header
(nvrd binds the LAN too, where headers are forgeable).
