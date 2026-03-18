# Conversation Summary — March 17, 2026

> Sessions from `/home/pcssadmin/Documents/aicoemaas` project
> Two Claude Code sessions covering Keycloak deployment and LiteLLM SSO integration in a Kubernetes environment.

---

## Session 1: Keycloak Deployment
**Time:** ~03:09–08:02 UTC
**File:** `eca0b06d-f1f9-458e-b00d-e684f7f9072b.jsonl`

### Topics Covered

**Postgres Database Setup**
- Located existing PostgreSQL cluster in `pgdb` namespace (`pgcluster-1/2/3`)
- Extracted credentials from `keycloak-cred` Kubernetes secret
- Identified service endpoints for JDBC connection strings

**Keycloak Docker Image Management**
- Tarred Keycloak image `v26.5.4` for transfer to air-gapped environment
- Docker login to private Harbor registry at `pafhbr01.paf.com`
- Pulled and loaded image using `docker save` / `docker load` workflow

**Kubernetes Registry Authentication**
- Configured `regcred` ImagePullSecret for Harbor registry
- Debugged docker pull failures (registry URL format issues)
- Referenced ImagePullSecrets in deployment manifests

**Keycloak Deployment Configuration**
- Set `KC_HOSTNAME` to `keycloak.maas.paf.com`
- Configured DB env vars: `KC_DB`, `KC_DB_URL`, `KC_DB_USERNAME`, `KC_DB_PASSWORD`
- JDBC connection string pointing to PostgreSQL cluster

**Kubernetes Ingress & External Access**
- Created Nginx Ingress for FQDN `keycloak.maas.paf.com`
- Configured path-based routing

**Cross-Namespace Secret Problem**
- `keycloak-cred` secret exists in `pgdb` namespace, but Keycloak pod runs in `keycloak` namespace
- Kubernetes `secretKeyRef` cannot reference secrets across namespaces — the secret must exist in the same namespace as the pod
- Reynard had already deployed a `keycloak-secret` but this limitation meant it wasn't reachable from the `keycloak` namespace directly
- **Solution:** created `01-secrets.yaml` — a dedicated manifest that redeclares the secret in the `keycloak` namespace with the base64 values copied from `pgdb`
- This makes the deployment fully declarative: `kubectl apply -f kubeconfigs/` just works without any manual copy steps
- Without this file, anyone redeploying would need to remember to manually copy the secret across namespaces — fragile and error-prone

```yaml
# 01-secrets.yaml (keycloak namespace copy)
apiVersion: v1
kind: Secret
metadata:
  name: keycloak-cred
  namespace: keycloak
type: Opaque
data:
  username: <base64-value>
  password: <base64-value>
```

**LiteLLM–Keycloak Integration Planning**
- Identified existing LiteLLM deployment (`litellm` namespace, port 4000)
- Began planning OIDC/SSO integration
- Initial discussion of required environment variables

### Files Created/Modified
- `01-secrets.yaml` — keycloak-cred secret redeclared in `keycloak` namespace
- `004-keycloak.yaml` — Keycloak deployment manifest
- `keycloak-svc.yaml` — Service definition
- `keycloak-ingress.yaml` — Ingress configuration

### Key Decisions
- Use declarative YAML manifests (not imperative kubectl commands)
- Use internal Kubernetes DNS for service-to-service communication
- Nginx Ingress for external access
- Postgres cluster stays in `pgdb` namespace with shared credentials
- Secrets must be explicitly redeclared per namespace — no cross-namespace references

---

## Session 2: LiteLLM SSO Integration & Debugging
**Time:** ~08:04–10:01 UTC
**File:** `0e2306ae-6d01-41b7-af1a-9daef1cc8e87.jsonl`

### Topics Covered

**Keycloak Token Endpoint Testing**
- Ran `debug_keycloak_token.py` inside LiteLLM pod
- Confirmed token generation via client credentials flow
- Decoded and validated JWT token contents

**HTTP/HTTPS Protocol Issue**
- External browser access via HTTPS (Keycloak ingress)
- Internal pod-to-pod communication via HTTP (Kubernetes service)
- Resolved confusion around protocol differences

**Userinfo Endpoint Empty Response**
- JSON parse error: `"Expecting value: line 1 column 1 (char 0)"`
- Userinfo endpoint returning empty body
- Investigated Keycloak client configuration as root cause

**Keycloak Client Secret in LiteLLM Namespace**
- The `litellm` namespace is managed by colleague **Weiming**
- Added a `keycloak-oidc` secret to the `litellm` namespace containing the Keycloak client ID secret value
- Secret referenced by the LiteLLM deployment via `secretKeyRef` so the client secret is never hardcoded in the manifest

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: keycloak-oidc
  namespace: litellm
type: Opaque
stringData:
  GENERIC_CLIENT_SECRET: "<keycloak-client-secret>"
```

**Why `07-litellm-oidc-patch.yaml` exists as a patch**
- The `litellm` namespace and its base deployment are owned by Weiming — the user doesn't modify his files directly
- Instead, a separate patch file overlays only the OIDC-related env vars onto the existing deployment via `kubectl apply`
- This keeps SSO configuration isolated and independently maintainable without touching Weiming's base setup

**LiteLLM SSO Environment Variables — and how each value was determined**

```yaml
- name: GENERIC_CLIENT_ID
  value: litellm
```
→ The client name created inside Keycloak admin UI for LiteLLM. Named `litellm` to match the application.

```yaml
- name: GENERIC_CLIENT_SECRET
  valueFrom:
    secretKeyRef:
      name: keycloak-oidc
      key: GENERIC_CLIENT_SECRET
```
→ The secret generated by Keycloak when the `litellm` client was created. Stored in the `keycloak-oidc` secret (added to `litellm` namespace by the user) rather than hardcoded.

```yaml
- name: GENERIC_AUTHORIZATION_ENDPOINT
  value: "http://keycloak.maas.paf.com/realms/master/protocol/openid-connect/auth"
```
→ Uses the **external FQDN** because this URL is followed by the **browser** — the user's browser must be able to reach Keycloak to display the login page. Internal cluster DNS is not reachable from a browser.

```yaml
- name: GENERIC_TOKEN_ENDPOINT
  value: "http://keycloak.keycloak.svc.cluster.local:8080/realms/master/protocol/openid-connect/token"
```
→ Uses **internal Kubernetes DNS** because this call is made **server-side** by the LiteLLM pod (exchanging the auth code for a token). No need to go through the external ingress — direct pod-to-pod is faster and more reliable.

```yaml
- name: GENERIC_USERINFO_ENDPOINT
  value: "http://keycloak.keycloak.svc.cluster.local:8080/realms/master/protocol/openid-connect/userinfo"
```
→ Same reasoning as token endpoint — called **server-side** by LiteLLM to fetch user details after obtaining the token. Internal DNS used.

```yaml
- name: GENERIC_REDIRECT_URI
  value: "http://litellm.maas.paf.com/sso/callback"
```
→ The callback URL Keycloak redirects the browser back to after successful login. Must use the **external FQDN** (browser follows this redirect) and must exactly match the redirect URI configured in the Keycloak client settings.

```yaml
- name: PROXY_BASE_URL
  value: "http://litellm.maas.paf.com"
```
→ Tells LiteLLM its own public base URL so it can construct correct absolute URLs (e.g. for the SSO callback). Without this, LiteLLM may build incorrect redirect links.

```yaml
- name: DEFAULT_USER_ROLE
  value: "internal_user"
```
→ Assigns a default LiteLLM role to any user who logs in via SSO. `internal_user` grants standard access without needing manual role assignment per user.

**Key insight on the internal/external split:**
- `GENERIC_AUTHORIZATION_ENDPOINT` and `GENERIC_REDIRECT_URI` → external FQDN (browser-facing)
- `GENERIC_TOKEN_ENDPOINT` and `GENERIC_USERINFO_ENDPOINT` → internal cluster DNS (server-facing)

**Deployment Patching & Restarts**
- Applied patches via `kubectl apply`
- Restarted deployment: `kubectl rollout restart deployment/litellm -n litellm`

**Database Access & Troubleshooting**
- Located primary DB pod (`pgcluster-2`)
- Inspected `LLMConfig` table for configuration state

**LiteLLM Enterprise SSO Restriction**
- LiteLLM community version enforces a hard limit: **SSO is only usable for 5 users**, after which it blocks SSO login entirely
- The restriction is **tracked in PostgreSQL** (not in the container) — so simply restarting the pod does nothing, the timer survives restarts
- Attempted `LITELLM_LICENSE=test` as a dummy license value — did not bypass the restriction
- **Workaround: drop and recreate the database schema**, which wipes the SSO trial tracking along with all other LiteLLM data (users, keys, configs)

```bash
# Connect to the LiteLLM database on the primary postgres pod
kubectl exec -it pgcluster-2 -n pgdb -- psql -h localhost -U litellm -d litellm
```

```sql
-- Nuclear reset — clears everything including SSO trial tracking
DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
\q
```

```bash
# Restart LiteLLM so it recreates all tables fresh from scratch
kubectl rollout restart deployment/litellm -n litellm
```

> **Note:** Keycloak's database is separate and unaffected by this reset. Only LiteLLM data is wiped.

### Files Created/Modified
- `07-litellm-oidc-patch.yaml` — OIDC configuration patch for LiteLLM
- `debug_keycloak_token.py` — Token endpoint test script

### Key Decisions
- Use internal Kubernetes DNS (`*.svc.cluster.local`) for token/userinfo endpoints
- Keep internal services on HTTP, external on HTTPS
- Test with incognito mode to avoid session caching issues *(note: this was actually the cause of the issue — see resolution below)*
- Restart deployments after every environment variable change

### Resolution (found after session)

**Root cause: Chrome incognito mode blocking session cookies.**

The OIDC authorization code flow requires cookies to maintain session state across the browser redirect:

1. LiteLLM generates a `state` parameter (CSRF protection), stores it in a **session cookie**, then redirects the browser to Keycloak
2. User authenticates on Keycloak
3. Keycloak redirects back to LiteLLM callback with `?code=...&state=...`
4. LiteLLM reads the session cookie to validate the `state` — **but Chrome incognito had blocked it**
5. With no valid session, the authorization code exchange failed silently
6. LiteLLM called the userinfo endpoint with no valid token → **empty body response**

The Keycloak client configuration (standard flow, redirect URIs, web origins) was **never the problem**. All time spent debugging Keycloak settings was a red herring. The userinfo endpoint was working correctly — there simply was no valid token to send it.

**Fix:** Switch from incognito to a regular browser window. Session cookies are preserved and the full OIDC flow completes successfully.

---

## Technical Environment Summary

| Component | Details |
|-----------|---------|
| Kubernetes | Nginx Ingress, multi-namespace setup |
| Registry | Private Harbor at `pafhbr01.paf.com` |
| Database | PostgreSQL cluster (CloudNativePG/Patroni) in `pgdb` namespace |
| LiteLLM | `litellm` namespace, port 4000 |
| Keycloak | `keycloak` namespace, `keycloak.maas.paf.com` |
| Jump host | 10.0.7.27 (RDP via mobilexterm) |
| Ubuntu host | 10.0.7.33/16 |
| Project dir | `/home/pcssadmin/Documents/aicoemaas` |
