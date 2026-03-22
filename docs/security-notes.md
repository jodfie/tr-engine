# Security Audit Notes

Developer reference for authentication architecture, known tradeoffs, and deployment guidance.

---

## Authentication Architecture Summary

tr-engine uses a layered authentication model:

| Mechanism | Format | Lifetime | Purpose |
|-----------|--------|----------|---------|
| JWT access token | `Bearer <token>` header | 1 hour | Stateless authN/authZ for API and dashboard |
| Refresh token | httpOnly cookie | 7 days | Silent token renewal without re-login |
| API keys | `tre_` prefix, Bearer header | No expiry (revocable) | Machine-to-machine / integration access |
| Legacy tokens | `AUTH_TOKEN` / `WRITE_TOKEN` env vars | Static | Backward-compatible read/write auth |

Access tokens carry the user's role (`viewer`, `operator`, `admin`). API keys are scoped to a role at creation time. Legacy tokens bypass role checks entirely -- `AUTH_TOKEN` grants read access, `WRITE_TOKEN` grants write access.

---

## Known Tradeoffs

### 1. localStorage for access tokens (XSS surface)

The dashboard stores the JWT access token in `localStorage` so it persists across page reloads. This is vulnerable to XSS.

**Why it's acceptable today:**
- The access token is read-only scoped
- It expires in 1 hour
- The refresh token is stored in an httpOnly cookie (not accessible to JS)

**When to revisit:** If the access token scope expands beyond read-only operations, switch to in-memory storage with a refresh-on-reload pattern (hit `/auth/refresh` on page load, keep the token only in a JS variable).

### 2. X-Forwarded-For trust model

Rate limiting and audit logs use the leftmost IP from the `X-Forwarded-For` header. This is correct when running behind a trusted reverse proxy (Caddy, Traefik, Cloudflare) that sets the header.

**Risk:** If tr-engine is exposed directly to the internet, clients can spoof `X-Forwarded-For` to bypass rate limits or poison logs. Always run behind a reverse proxy in production.

### 3. /api/v1/query endpoint (operator+ arbitrary SQL)

The query endpoint gives `operator` and `admin` users arbitrary read-only SQL access. This is intentional for debugging and ad-hoc analysis.

**Implication:** Operators can read all table data, including `users` (which contains password hashes). If this is a concern for your deployment, restrict the endpoint to `admin` role only.

---

## Future Security Enhancements

Not yet implemented. Listed in rough priority order:

- **Account lockout** -- Track per-account failed login attempts. Lock after 5 failures for 15 minutes.
- **Password complexity** -- Currently enforces only 8-character minimum. Add rules for mixed case, digits, symbols.
- **OAuth/OIDC integration** -- Enterprise SSO support (Okta, Azure AD, Keycloak).
- **Resource-level permissions** -- Per-system and per-talkgroup ACLs so roles can be scoped to specific data.
- **Trusted proxy configuration** -- Explicit allowlist of proxy IPs for `X-Forwarded-For` validation instead of blanket trust.

---

## Deployment Checklist

- [ ] Set `JWT_SECRET` in `.env` -- do not rely on auto-generation (a random secret is generated at startup if missing, but it changes on every restart, invalidating all tokens)
- [ ] Set `WRITE_TOKEN` for two-tier legacy auth if legacy clients need write access
- [ ] Run behind a reverse proxy (Caddy, Traefik, Cloudflare Tunnel) -- never expose tr-engine directly
- [ ] Use HTTPS -- the `Secure` flag on the refresh cookie depends on it; without HTTPS the cookie won't be sent
- [ ] Set `CORS_ORIGINS` to the specific origins that need access (avoid `*` in production)
- [ ] Review `RATE_LIMIT_RPS` and `RATE_LIMIT_BURST` -- defaults may be too generous for public-facing deployments
