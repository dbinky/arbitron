# Tunnels, proxies, and TLS

This applies to **shared** servers (scenarios **d** and **e**) that you want to
reach from outside the host. Local installs (a/b/c) bind loopback and need none
of this.

There are two ways to terminate TLS, chosen at the **TLS model** Q&A step:

1. **Behind a tunnel/proxy** (`tunnel`) — recommended for public exposure.
2. **Direct app-TLS** (`direct` or `autocert`) — the binary serves HTTPS itself.

## Operator runs the tunnel

**The installer never provisions or touches a tunnel or reverse proxy.** It only
writes config and prints guidance — you run your own edge.
[cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
is the likely initial path, but any TLS-terminating, authenticating proxy works.

When you choose the `tunnel` TLS model the installer:

- sets `server.tls.enabled: false` (the app serves plain HTTP on the origin),
- sets `server.publicbaseurl` to the public `https://…` URL your tunnel exposes
  (so cookies, OIDC redirects, and generated links are correct),
- enables **Secure cookies** (`auth.session.cookiesecure: true`), and
- prints the origin to point your tunnel at.

You then point your tunnel at the loopback/origin `http://<host>:<port>`.

### Recommended edge path

Use the `edge-local` auth model **with** the `tunnel` TLS model: the installer
**forces a loopback bind** so Arbitron is reachable **only** through your
authenticated tunnel, and the single shared admin sits behind that edge.

Minimal cloudflared ingress pointing at the loopback origin:

```yaml
# ~/.cloudflared/config.yml (operator-managed; NOT written by the installer)
tunnel: <your-tunnel-id>
credentials-file: /home/you/.cloudflared/<your-tunnel-id>.json
ingress:
  - hostname: arbitron.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```

Set the install-time public base URL to `https://arbitron.example.com`.

If instead you want a per-user identity at the app (no shared admin), choose the
**OIDC org** auth model — see [security.md](security.md#local-mode--tls-exposure-and-allowunauthenticated)
for why OIDC is the safer choice for direct public exposure.

## Direct app-TLS (`server.tls`)

Choose `direct` or `autocert` if you want the binary to terminate TLS itself
(no separate proxy). This writes a `server.tls` block. The installer pre-enforces
the binary's fail-fast validation rules so the server never fails to load:

- **`enabled: true` requires either** a cert+key pair **or** an autocert domain.
- **`certfile` and `keyfile` are both-or-neither** — a cert without a key (or
  vice versa) is rejected at load. The installer aborts if you give only one.
- **cert/key and autocert are mutually exclusive** — you get one or the other.

### BYO cert/key (`direct`)

You supply absolute paths to your certificate and private key (e.g. an
internal-CA cert):

```yaml
server:
  tls:
    enabled: true
    certfile: "/etc/arbitron/tls/server.crt"
    keyfile:  "/etc/arbitron/tls/server.key"
```

### Autocert (Let's Encrypt)

The binary obtains and renews a managed certificate via ACME:

```yaml
server:
  port: 443
  tls:
    enabled: true
    autocertdomain:   "arbitron.example.com"
    autocertcachedir: "/var/lib/arbitron/autocert"
```

Autocert requires:

- **public DNS** for the domain pointing at the host, and
- **port 443 reachable** (plus `:80` for the HTTP-01 challenge / redirect). The
  installer **forces `server.port` to 443** when you pick autocert.

> Direct app-TLS in **local mode** on a **non-loopback host** exposes an
> unauthenticated admin API and requires the explicit
> `server.tls.allowunauthenticated` escape hatch. See
> [security.md](security.md#local-mode--tls-exposure-and-allowunauthenticated)
> before choosing this combination.
