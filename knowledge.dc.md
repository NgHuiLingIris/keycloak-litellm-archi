# Knowledge: Keycloak + LDAP/AD Integration

---

## Stack Overview

| Service    | Role                                                        | Port      |
|------------|-------------------------------------------------------------|-----------|
| Samba DC   | Fake Active Directory (LDAP/AD domain controller)           | 389, 636  |
| PostgreSQL | Database backend for Keycloak and LiteLLM                   | —         |
| Keycloak   | Identity/SSO provider; federates users from AD              | 8080      |
| Ollama     | Local LLM inference server (dev stand-in for vLLM)          | 11434     |
| LiteLLM    | LLM proxy/gateway; uses Keycloak for auth/access control    | 4000      |

Samba DC is a stand-in for a production Azure AD / on-prem AD. The integration patterns are identical.

---

## LDAP User Federation in Keycloak

Keycloak's **User Federation** allows it to delegate user storage to an external directory
like Active Directory. Instead of managing users natively in Keycloak, users live in AD and
Keycloak syncs or looks them up on demand.

- Users log in to Keycloak with their AD credentials
- Keycloak queries AD to verify the password and retrieve user attributes
- Group memberships from AD can be mapped to Keycloak roles

---

## Bind Type / Bind DN / Bind Credentials

These are the credentials Keycloak uses to **log into AD as a service account** — not for
end users, but for Keycloak itself to query the directory (e.g. to look up users, sync them,
validate group membership). Think of it as Keycloak's own AD login that runs in the background.

| Field | Meaning |
|---|---|
| Bind type | Auth method — use `simple` (username + password) |
| Bind DN | The AD account Keycloak logs in as (UPN format: `user@domain`) |
| Bind credentials | Password for that account |

In production, use a dedicated read-only service account instead of Administrator.

---

## LDAP vs LDAPS vs StartTLS

| Mode | URL format | Port | Encryption |
|---|---|---|---|
| Plain LDAP | `ldap://host:389` | 389 | None — rejected by AD by default |
| LDAPS | `ldaps://host:636` | 636 | Implicit TLS from the start |
| StartTLS | `ldap://host:389` + flag | 389 | Negotiated after initial connection |

AD refuses plain LDAP binds — it requires transport encryption. Use `ldaps://` on port 636.

---

## TLS Certificate and Truststore

When Keycloak connects to LDAPS, it validates the server's TLS certificate against a trusted CA.
Samba uses a self-signed certificate, so its CA must be explicitly trusted by Keycloak.

**How it works:**
1. Samba's CA cert is placed in `keycloak-trust/samba-ca.pem`
2. Keycloak loads it via `KC_TRUSTSTORE_PATHS=/opt/keycloak/truststore/samba-ca.pem`
3. `keycloak-trust/` is mounted into the Keycloak container

**The cert CN and SAN must match the hostname Keycloak connects to (`samba-dc`).**
Samba auto-generates its cert using the machine hostname, which won't match `samba-dc`.
This is why a custom cert must be generated on first setup (see `setup.dev`).

Common cert errors:
| Error | Cause |
|---|---|
| `PKIX path building failed` | Keycloak doesn't trust the CA — add cert to truststore |
| `No name matching samba-dc found` | Cert CN/SAN doesn't include `samba-dc` — regenerate cert |

---

## Common LDAP Errors and Fixes

| Error | Cause | Fix |
|---|---|---|
| `BindSimple: Transport encryption required` | Plain LDAP on port 389 | Use `ldaps://` on port 636 |
| `PKIX path building failed` | CA cert not trusted | Add CA cert to `KC_TRUSTSTORE_PATHS` |
| `No name matching samba-dc found` | Cert CN/SAN mismatch | Regenerate cert with correct CN/SAN |
| `LDAP connection has been closed` | Bind dropped after connect | Wrong password or timing — reset with `samba-tool` |
| `Invalid credentials (49)` | Wrong bind password | Re-run `samba-tool user setpassword` |
| `user_not_found` on valid user | `Referral=follow` causes Keycloak to chase AD referrals to `ad.example.com` which is unresolvable inside Docker | Set Referral to `ignore` in LDAP federation settings |
| `Couldn't resolve groups from LDAP` / Internal Server Error on login | `Preserve Group Inheritance` is ON — Keycloak tries to resolve AD built-in groups (e.g. `Group Policy Creator Owners`) whose members include users treated as groups, causing a `GroupTreeResolveException` during token generation | Set `Preserve Group Inheritance` OFF and `Ignore Missing Groups` ON in the `litellm-groups` LDAP group mapper |
| `CODE_TO_TOKEN_ERROR: invalid_client_credentials` / Internal Server Error after login redirect | `GENERIC_CLIENT_SECRET` in `docker-compose.yml` does not match the client secret Keycloak has for the `litellm` client — happens when the client is recreated or the secret is rotated without updating the env var | Go to **Clients → litellm → Credentials**, copy the current secret, update `GENERIC_CLIENT_SECRET` in `docker-compose.yml`, then restart: `docker compose up -d litellm` |
| `404 Not Found` on `/sso/login` | The `/sso/login` endpoint does not exist in LiteLLM — this was never a valid route | Use `/sso/key/generate` as the SSO login URL |

Note: The Samba admin password is lost on container restart — always reset it after restarting.

**Critical LDAP federation settings that cause login failures if wrong:**

| Field | Wrong value | Correct value | Why |
|---|---|---|---|
| Referral | `follow` | `ignore` | AD returns referrals to `ad.example.com` — unresolvable in Docker, causes `user_not_found` |
| Username LDAP attribute | `cn` | `sAMAccountName` | `cn` may match but `sAMAccountName` is the standard AD login attribute |

---

## Migrating from Samba DC to Company Active Directory

### AD type matters

| Type | LDAP supported? | Notes |
|---|---|---|
| On-premises Windows AD | Yes | Standard LDAP/LDAPS on port 389/636 |
| Azure AD DS (Domain Services) | Yes | Managed AD, supports LDAP/LDAPS |
| Azure AD / Entra ID (cloud-only) | No | Must use OIDC/SAML instead of LDAP |

If your company is on a devnet (airgapped), it is almost certainly on-prem AD or Azure AD DS.

### Questions to ask your IT/AD team

1. **What is the LDAP endpoint?** (hostname/IP, port 636 for LDAPS)
2. **What is the domain name?** (e.g. `company.com`) — determines `DC=company,DC=com`
3. **Can I get a read-only service account?** UPN format: `svc-keycloak@company.com`
4. **What OU are users stored in?** e.g. `CN=Users,DC=company,DC=com` or a custom OU
5. **Is LDAPS enforced?** If yes, request the CA certificate used to sign the DC's TLS cert
6. **Are there firewall rules to open?** Port 636 outbound from your Keycloak server

### What changes in Keycloak when migrating

Only the LDAP federation settings change. Everything else stays the same.

| Field | Samba (dev) | Company AD (prod) |
|---|---|---|
| Connection URL | `ldaps://samba-dc:636` | `ldaps://dc01.company.com:636` |
| Bind DN | `Administrator@ad.example.com` | `svc-keycloak@company.com` |
| Bind credentials | `Admin1234!` | service account password |
| Users DN | `CN=Users,DC=ad,DC=example,DC=com` | `OU=Staff,DC=company,DC=com` |
| Truststore | self-signed Samba CA cert | corporate CA cert from IT |

---

## OAuth2/OIDC Grant Types

Grant types are different flows for how an application gets an access token from Keycloak.

| Grant Type | Use Case | Recommendation |
|---|---|---|
| **Standard Flow** (Authorization Code) | Browser-based login with redirect. Most secure. | ON — powers SSO redirect |
| **Direct Access Grants** (Password) | App sends credentials directly to Keycloak. No redirect. | ON for dev/testing |
| **Implicit Flow** | Deprecated browser flow. Token returned in URL fragment. | OFF — security risk |
| **Service Account Roles** | Client authenticates as itself (no user). Server-to-server. | ON — backend calls |
| **Standard Token Exchange** | Exchange one token for another / impersonation. | OFF unless needed |
| **Device Authorization Grant** | For devices with no browser (TV, CLI). | OFF |
| **OIDC CIBA Grant** | Decoupled auth via push notification on separate device. | OFF |

---

## Keycloak OIDC Client URL Fields

When registering an application as an OIDC client in Keycloak:

| Field | Purpose |
|---|---|
| Root URL | Base URL of the app — Keycloak uses this to build absolute redirect URLs |
| Home URL | Where Keycloak links back to when users click "back to app" |
| Valid redirect URIs | URLs Keycloak is allowed to redirect to after login (use `/*` wildcard in dev) |
| Valid post logout redirect URIs | URLs Keycloak is allowed to redirect to after logout |
| Web origins | Allowed CORS origins — lets the app's frontend make requests to Keycloak |

In production, replace `/*` wildcards with the exact callback URL.

---

## Keycloak LDAP Federation Field Reference

### General Settings

**Edit Mode**
| Value | Meaning |
|---|---|
| `READ_ONLY` | Keycloak only reads from AD. AD is the source of truth. **Use this.** |
| `WRITABLE` | Keycloak can update AD. Requires write permissions on the service account. |
| `UNSYNCED` | Users imported once, then managed in Keycloak only. |

**Users DN** — Base location in AD where Keycloak searches for users.
Derived by splitting the realm into DC components: `AD.EXAMPLE.COM` → `DC=ad,DC=example,DC=com`

**Username LDAP Attribute** — Use `sAMAccountName` (short login) or `userPrincipalName` (full UPN).

**RDN LDAP Attribute** — Use `cn`.

**UUID LDAP Attribute** — Use `objectGUID` for AD.

**User Object Classes** — `person, organizationalPerson, user`

**User LDAP Filter** — Leave blank for all users. Example to filter by group:
`(memberOf=CN=developers,CN=Users,DC=ad,DC=example,DC=com)`

**Search Scope** — Use `Subtree` to search Users DN and all sub-OUs.

**Read Timeout** — `10000` (10 seconds).

**Pagination** — Enable, page size `100`.

**Referral** — Use `ignore` to avoid errors from unreachable AD partitions.

### Synchronization Settings

| Setting | Value | Notes |
|---|---|---|
| Import Users | ON | Imports AD users into Keycloak on first login or sync |
| Sync Registrations | OFF | Only needed if WRITABLE and users self-register |
| Batch Size | `1000` | Users per page during full sync |
| Periodic Full Sync | ON, `86400`s | Syncs all users once daily |
| Periodic Changed Users Sync | ON, `3600`s | Syncs only modified users every hour |
| Remove Invalid Users | ON | Removes Keycloak users no longer found in AD |

### Kerberos Integration

Leave both Kerberos settings OFF unless your environment has a Kerberos KDC configured.

### Cache Settings

Use `DEFAULT` or `EVICT_DAILY`.

### Advanced Settings

| Setting | Value | Notes |
|---|---|---|
| LDAPv3 Password Modify Extended Operation | ON | Required for password changes with AD |
| Validate Password Policy | OFF | Let AD be the authority on password rules |
| Trust Email | ON | Email from AD is trusted — no re-verification needed |
| Connection Trace | OFF | Turn ON only when debugging LDAP issues |

---

## PostgreSQL Multiple Databases

The official Postgres Docker image only creates one database on first start (`POSTGRES_DB`).
To share one Postgres container across multiple services (Keycloak + LiteLLM), additional
databases must be created via an init script.

`scripts/pg_multiple_databases.sh` is mounted into `docker-entrypoint-initdb.d/` and runs
automatically on **first container start**. It reads `POSTGRES_MULTIPLE_DATABASES` (comma-separated)
and creates each listed database.

In this project:
- `POSTGRES_DB=keycloak` — created by Postgres automatically
- `POSTGRES_MULTIPLE_DATABASES=litellm` — created by the script

**Caveat:** Init scripts only run on a fresh volume. If the `kc_db` volume already exists,
the script is skipped. Create missing databases manually instead:
```
docker exec keycloak-db psql -U keycloak -c "CREATE DATABASE litellm;"
```

---

## LiteLLM User Roles

SSO users from Keycloak are assigned a role in LiteLLM based on their AD group membership.

| Role | Create own API keys | Manage users/teams/models |
|---|---|---|
| `internal_user_viewer` | No | No |
| `internal_user` | Yes (own keys only) | No |
| `proxy_admin_viewer` | No | Read-only admin |
| `proxy_admin` | Yes | Yes |

### Group-based role assignment (recommended)

LiteLLM reads a groups claim from the Keycloak userinfo response and maps group names to roles.
This updates roles on **every login** — existing users are re-evaluated each time they log in.

Requires `STORE_MODEL_IN_DB: "True"` in `docker-compose.yml` under the `litellm` service.

Configuration is set via the LiteLLM API (run once after first `docker compose up`):
```bash
curl -s -X PATCH http://localhost:4000/update/sso_settings \
  -H "Authorization: Bearer sk-1234" \
  -H "Content-Type: application/json" \
  -d '{
    "role_mappings": {
      "provider": "generic",
      "group_claim": "litellm-groups",
      "default_role": "internal_user_viewer",
      "roles": {"internal_user": ["developers"]}
    }
  }'
```

`role_mappings` fields:
| Field | Value | Meaning |
|---|---|---|
| `provider` | `generic` | Must be `generic` or `okta` for group-based mapping |
| `group_claim` | `litellm-groups` | Claim name in the Keycloak userinfo that contains the groups array — must match the Token Claim Name set in the Keycloak client mapper |
| `default_role` | `internal_user_viewer` | Role for users not in any mapped group |
| `roles` | `{"internal_user": ["developers"]}` | Map LiteLLM role → list of AD group names |

Requires Keycloak to include group membership in the userinfo response (see Keycloak setup below).

### Keycloak: LDAP Group Mapper (syncs AD groups to Keycloak)

Under **User Federation → LDAP → Mappers → Add mapper**:

| Field | Value |
|---|---|
| Name | `litellm-groups` |
| Mapper Type | `group-ldap-mapper` |
| LDAP Groups DN | `CN=Users,DC=ad,DC=example,DC=com` |
| Group Object Classes | `group` |
| Membership LDAP Attribute | `member` |
| Membership Attribute Type | `DN` |
| Membership User LDAP Attribute | `cn` |
| Groups Path | `/` |
| Preserve Group Inheritance | OFF |
| Ignore Missing Groups | ON |

**Preserve Group Inheritance** — when ON, Keycloak resolves the full nested group tree from AD. AD's built-in groups (e.g. `Group Policy Creator Owners`) contain the `Administrator` user as a member; Keycloak tries to find `Administrator` as a group, fails, and throws an exception that surfaces as an Internal Server Error on every login. Turn this OFF to flatten the group list.

**Ignore Missing Groups** — when ON, Keycloak skips any group reference it cannot resolve instead of aborting. Keep this ON as a safeguard against other built-in AD objects that are not standard groups.

After saving, click **Sync LDAP Groups to Keycloak**.

### Keycloak: Client Group Mapper (includes groups in userinfo)

Under **Clients → litellm → Client Scopes → litellm-dedicated → Mappers → Configure a new mapper**:

| Field | Value |
|---|---|
| Mapper Type | `Group Membership` |
| Name | `litellm-groups` |
| Token Claim Name | `litellm-groups` |
| Full group path | OFF |
| Add to ID token | ON |
| Add to access token | ON |
| Add to userinfo | ON |

### Default role fallback (new users with no group match)

`litellm_config.yaml` sets the role for new users when `role_mappings` is not configured or when
the SSO response provides no groups. With `role_mappings`, the `default_role` field takes precedence.

```yaml
litellm_settings:
  default_internal_user_params:
    user_role: "internal_user_viewer"
```

**Note:** `DEFAULT_USER_ROLE` as a docker-compose env var does not exist in LiteLLM — it has no effect.

### Fixing roles for existing users manually

If a user exists with the wrong role and hasn't logged in yet after the group mapper is configured:
```sql
UPDATE "LiteLLM_UserTable" SET user_role = 'internal_user_viewer'
WHERE user_id = 'charlie';
```
Or via the LiteLLM admin UI: Users → select user → change role.

---

## SSO Token Issuer Mismatch (Internal Server Error)

**Symptom:** Login succeeds in Keycloak but LiteLLM returns Internal Server Error.

**Error in Keycloak logs:**
```
Invalid token issuer. Expected 'http://keycloak:8080/realms/master'
```

**Root cause:** The token issuer is determined by the URL the browser used to access Keycloak
(`localhost:8080`). When LiteLLM validates the token by calling the userinfo endpoint at
`keycloak:8080`, Keycloak expects the issuer to be `http://keycloak:8080/...` — but the token
says `http://localhost:8080/...`. Mismatch → error.

**Fix:** Pin Keycloak's hostname so all tokens always use `localhost` as issuer, and route
LiteLLM's server-side calls through `host.docker.internal` (which resolves to the host machine):

In `docker-compose.yml` under the `keycloak` service:
```yaml
KC_HOSTNAME: "localhost"
KC_HOSTNAME_PORT: "8080"
```

In `docker-compose.yml` under the `litellm` service:
```yaml
GENERIC_TOKEN_ENDPOINT: "http://host.docker.internal:8080/realms/master/protocol/openid-connect/token"
GENERIC_USERINFO_ENDPOINT: "http://host.docker.internal:8080/realms/master/protocol/openid-connect/userinfo"
extra_hosts:
  - "host.docker.internal:host-gateway"
```

The `GENERIC_AUTHORIZATION_ENDPOINT` stays as `localhost:8080` (browser-facing).
The token and userinfo endpoints use `host.docker.internal:8080` (server-side from LiteLLM container)
so the issuer in all tokens always matches `http://localhost:8080/realms/master`.

---

## LiteLLM Diagnostic curl Commands

Useful curl commands for verifying LiteLLM and SSO state.

**Check if an endpoint exists and where it redirects**
```bash
curl -s -o /dev/null -w "%{http_code} -> %{redirect_url}" http://localhost:4000/sso/key/generate
```
`-o /dev/null` discards the body. `-w` prints only the HTTP status code and redirect URL.
Use this to quickly confirm whether an endpoint exists (404 = not found) or redirects (307).

**Follow a redirect and report final status**
```bash
curl -s -o /dev/null -w "%{http_code} -> %{redirect_url}" -L http://localhost:4000/ui/login/
```
`-L` tells curl to follow redirects. Reports the final status code after all hops.

**Check LiteLLM UI and SSO configuration**
```bash
curl -s http://localhost:4000/litellm/.well-known/litellm-ui-config
```
Returns a JSON object showing whether SSO is configured, the proxy base URL, and whether
auto-redirect to SSO is enabled. Key fields:
- `sso_configured: true/false` — whether GENERIC_* env vars were picked up
- `auto_redirect_to_sso: true/false` — whether the login page auto-redirects to Keycloak

**List all SSO-related API endpoints**
```bash
curl -s http://localhost:4000/openapi.json | python3 -c "import json,sys; paths=[p for p in json.load(sys.stdin)['paths'] if 'sso' in p.lower()]; print('\n'.join(paths))"
```
Fetches LiteLLM's OpenAPI spec and filters for SSO-related paths. Use this to discover
what SSO endpoints are available in the running version.

**Check SSO readiness (no auth)**
```bash
curl -s http://localhost:4000/sso/readiness
```
Returns 401 if SSO is active (requires an API key). Returns 200 if SSO is not configured.

**Check SSO UI settings (with master key)**
```bash
curl -s -H "Authorization: Bearer sk-1234" http://localhost:4000/sso/get/ui_settings
```
Returns the full SSO configuration as seen by LiteLLM. Key field:
- `SSO_ENABLED: true` — confirms SSO is active and Keycloak integration is working

---

## LiteLLM vs Ollama vs vLLM

| | Ollama | vLLM |
|---|---|---|
| Target use | Local/dev, consumer | Production serving |
| CPU support | Yes, native | No — requires GPU |
| Throughput | Low | High (batching, paged attention) |
| OpenAI-compatible API | Yes | Yes |

Ollama is used in dev as a CPU-friendly stand-in. In production, swap to vLLM on a GPU machine.
LiteLLM abstracts the difference — only `litellm_config.yaml` needs updating.
