# DevNet Migration Checklist — Azure AD + vLLM

Your task in the company dev environment is to deploy **Keycloak + LiteLLM** only.
Samba DC, Ollama, and the TLS cert workflow are not needed — Azure AD is the identity
provider and vLLM is already running.

* * *

## What You Will Deploy

| Pod | Needed | Reason |
|---|---|---|
| postgres | YES | Keycloak and LiteLLM both need a database |
| keycloak | YES | Your main task |
| litellm | YES | SSO integration target |
| samba-dc | NO | Replaced by Azure AD |
| ollama | NO | Replaced by company vLLM |

* * *

## Section 1 — Values to Retrieve from the Company DevNet

### 1.1 Kubernetes / Cluster

| Value | How to retrieve | Used in |
|---|---|---|
| **Namespace** | Ask your cluster admin or check `kubectl get namespaces` | All `namespace:` fields in every manifest |
| **StorageClass** | `kubectl get storageclass` — use the one marked `(default)` | PVC `storageClassName:` (currently omitted = use default) |
| **Cluster domain** | Usually `cluster.local` — confirm with `kubectl exec ... -- cat /etc/resolv.conf` | All `*.svc.cluster.local` DNS names |
| **Image pull policy / private registry** | Ask DevOps — check if images need to be mirrored | `image:` fields + add `imagePullSecrets:` if needed |
| **Ingress class** | `kubectl get ingressclass` | Needed if exposing via Ingress instead of port-forward |
| **TLS cert / domain for Keycloak** | Ask DevOps for the external hostname (e.g. `keycloak.devnet.company.com`) | `KC_HOSTNAME` in `04-keycloak.yaml` |

### 1.2 Azure AD (Entra ID)

Keycloak connects to Azure AD as an **Identity Provider** (OIDC), not LDAP.
You will configure this inside Keycloak's UI after deployment.

| Value | How to retrieve | Used in |
|---|---|---|
| **Tenant ID** | Azure Portal → Entra ID → Overview → Tenant ID (GUID) | Keycloak IdP OIDC Discovery URL |
| **Azure AD OIDC Discovery URL** | `https://login.microsoftonline.com/<TENANT_ID>/v2.0/.well-known/openid-configuration` | Keycloak: Identity Providers → Add → OpenID Connect → Discovery URL |
| **App Registration Client ID** | Azure Portal → Entra ID → App Registrations → your app → Application (client) ID | Keycloak IdP: Client ID field |
| **App Registration Client Secret** | Azure Portal → App Registration → Certificates & Secrets → New client secret | Keycloak IdP: Client Secret field |
| **Redirect URI to register** | `https://<KEYCLOAK_HOSTNAME>/realms/master/broker/azure-ad/endpoint` | Register this in Azure App Registration → Redirect URIs |
| **Group claim configuration** | Azure Portal → App Registration → Token Configuration → Add groups claim → Security groups | Required for group-based role mapping in LiteLLM |
| **User principal format** | Check with AD admin — usually `user@company.com` | Keycloak username mapper configuration |

> **Note on LDAP vs OIDC:**
> If the company uses **Azure AD Domain Services** (AADDS — a managed domain controller),
> LDAP federation is possible and you would use the LDAP approach from `setup.dc.md`.
> Confirm with your AD admin whether AADDS is available. If it is, the Connection URL
> becomes `ldaps://<AADDS_IP>:636` and the TLS cert workflow applies.
> For standard Azure AD (Entra ID without AADDS), use the OIDC Identity Provider approach above.

### 1.3 vLLM

| Value | How to retrieve | Used in |
|---|---|---|
| **vLLM base URL** | Ask the team running vLLM — e.g. `http://vllm.devnet.svc.cluster.local:8000` or an external URL | `api_base` in `litellm-config` ConfigMap (`06-litellm.yaml`) |
| **vLLM API key** | Ask the team — vLLM may use `--api-key` or be open internally | `api_key` in `litellm_params` in the ConfigMap |
| **Available model names** | `curl http://<vllm-url>/v1/models` | `model:` field in `litellm_params` (format: `openai/<model-name>`) |

### 1.4 Postgres (if using an existing company database)

If the company has a managed postgres (RDS, Azure Database for PostgreSQL, CloudNativePG):

| Value | How to retrieve | Used in |
|---|---|---|
| **Host / port** | Ask DevOps | `KC_DB_URL` in `04-keycloak.yaml`, `DATABASE_URL` in `06-litellm.yaml` |
| **Keycloak DB name, user, password** | Ask DevOps or create them | Same |
| **LiteLLM DB name** | Ask DevOps or create it | `DATABASE_URL` in `06-litellm.yaml` |

If no managed postgres is available, deploy `02-postgres.yaml` as-is.

### 1.5 LiteLLM Public URL

| Value | How to retrieve | Used in |
|---|---|---|
| **Public URL for LiteLLM** | Will be assigned by your Ingress or NodePort — e.g. `https://litellm.devnet.company.com` | `PROXY_BASE_URL`, `GENERIC_REDIRECT_URI`, Keycloak client URLs |

* * *

## Section 2 — What to Replace in Each Config File

### `00-namespace.yaml`
```
name: keycloak  →  name: <YOUR_NAMESPACE>
```

### `01-secrets.yaml`
```
namespace: keycloak  →  <YOUR_NAMESPACE>
LITELLM_MASTER_KEY: sk-1234  →  generate a strong random key
```
Leave `GENERIC_CLIENT_SECRET` as placeholder — updated after Keycloak client is created.

### `02-postgres.yaml`
```
namespace: keycloak  →  <YOUR_NAMESPACE>
POSTGRES_PASSWORD: keycloak  →  use a strong password
```
If using company-managed postgres, **skip this file entirely**.

### `04-keycloak.yaml`

| Current value | Replace with |
|---|---|
| `namespace: keycloak` | `<YOUR_NAMESPACE>` |
| `postgres.keycloak.svc.cluster.local` | `postgres.<YOUR_NAMESPACE>.svc.cluster.local` (or managed DB host) |
| `KC_DB_PASSWORD: keycloak` | your postgres password |
| `KC_HOSTNAME: localhost` | your Keycloak public hostname (e.g. `keycloak.devnet.company.com`) |
| `KC_HOSTNAME_PORT: "8080"` | `443` if behind TLS ingress |
| `KC_HOSTNAME_STRICT_HTTPS: "false"` | `"true"` if serving over HTTPS |
| `KEYCLOAK_ADMIN_PASSWORD: admin` | a strong admin password |
| Remove `KC_TRUSTSTORE_PATHS` entirely | Not needed — Azure AD uses public TLS certs trusted by Java |
| Remove `truststore` volume and volumeMount | Not needed — no self-signed cert to trust |
| Remove `keycloak-truststore` ConfigMap reference | Not needed |
| `nodePort: 30080` | Remove NodePort, use ClusterIP + Ingress in production |

### `06-litellm.yaml`

| Current value | Replace with |
|---|---|
| `namespace: keycloak` | `<YOUR_NAMESPACE>` |
| `http://ollama.keycloak.svc.cluster.local:11434` | `http://<VLLM_URL>` |
| `model: ollama/tinyllama` | `model: openai/<VLLM_MODEL_NAME>` |
| `model_name: tinyllama` | the model name users will call |
| `postgres.keycloak.svc.cluster.local` | `postgres.<YOUR_NAMESPACE>.svc.cluster.local` (or managed DB host) |
| `keycloak.keycloak.svc.cluster.local` | `keycloak.<YOUR_NAMESPACE>.svc.cluster.local` |
| `GENERIC_AUTHORIZATION_ENDPOINT` | `https://<KEYCLOAK_PUBLIC_HOST>/realms/master/protocol/openid-connect/auth` |
| `GENERIC_TOKEN_ENDPOINT` | `http://keycloak.<YOUR_NAMESPACE>.svc.cluster.local:8080/realms/master/protocol/openid-connect/token` |
| `GENERIC_USERINFO_ENDPOINT` | `http://keycloak.<YOUR_NAMESPACE>.svc.cluster.local:8080/realms/master/protocol/openid-connect/userinfo` |
| `GENERIC_REDIRECT_URI` | `https://<LITELLM_PUBLIC_HOST>/sso/callback` |
| `PROXY_BASE_URL` | `https://<LITELLM_PUBLIC_HOST>` |
| `nodePort: 30000` | Remove NodePort, use ClusterIP + Ingress in production |

If vLLM requires an API key, add to the `litellm_params` block in the ConfigMap:
```yaml
litellm_params:
  model: openai/<model-name>
  api_base: http://<vllm-url>:8000
  api_key: <VLLM_API_KEY>
```

* * *

## Section 3 — Keycloak Configuration for Azure AD (UI Steps)

After Keycloak is running, instead of configuring LDAP Federation, configure an
**Identity Provider** to delegate login to Azure AD.

### 3.1 Add Azure AD as an Identity Provider

1. Go to **Identity Providers → Add provider → OpenID Connect v1.0**
2. Set **Alias**: `azure-ad`
3. Set **Display name**: `Company Azure AD` (shown on login button)
4. Paste the **Discovery URL**:
   `https://login.microsoftonline.com/<TENANT_ID>/v2.0/.well-known/openid-configuration`
5. Click **Import** — Keycloak auto-fills all endpoints
6. Set **Client ID**: `<APP_REGISTRATION_CLIENT_ID>`
7. Set **Client Secret**: `<APP_REGISTRATION_CLIENT_SECRET>`
8. Click **Save**
9. Copy the **Redirect URI** shown in the page — register it in Azure App Registration

### 3.2 Map Azure AD groups to Keycloak groups (for role mapping)

1. Go to **Identity Providers → azure-ad → Mappers → Add mapper**
2. Mapper type: **Hardcoded Group**  or  **Claim to Group** depending on what Azure AD sends
3. For group-based role mapping to work in LiteLLM, the `litellm-groups` claim must be populated
   — configure this mapper to forward the Azure AD group claim into Keycloak groups

> Confirm with your AD admin what group claims are available and what format they take.
> Azure AD sends group Object IDs by default; configure Token Configuration in App Registration
> to send group names instead (or map OIDs to Keycloak group names manually).

### 3.3 Create LiteLLM OIDC client

Same steps as `setup.kube.md` section 16, with the public LiteLLM URL substituted.

* * *

## Section 4 — SSO Endpoint Note for Production

In the local dev setup, `KC_HOSTNAME=localhost` was required because there was no real
public hostname. In production:

```
KC_HOSTNAME = keycloak.devnet.company.com   (your actual public hostname)
KC_HOSTNAME_PORT = 443
KC_HOSTNAME_STRICT_HTTPS = true
```

With this, all three OIDC endpoints (auth, token, userinfo) use the same public URL and
the split-endpoint workaround is not needed. Set all three LiteLLM GENERIC_* endpoints to:
```
https://keycloak.devnet.company.com/realms/master/protocol/openid-connect/<auth|token|userinfo>
```

* * *

## Section 5 — Files Not Needed in Production

| File | Skip if |
|---|---|
| `03-samba.yaml` | Always — Azure AD replaces Samba |
| `05-ollama.yaml` | Always — company vLLM is already running |
| TLS cert generation steps | Always — Azure AD uses trusted public certs |
| `keycloak-truststore` ConfigMap | Always — Java trusts Microsoft's CA by default |
| `KC_TRUSTSTORE_PATHS` env var | Always — remove from `04-keycloak.yaml` |
| `02-postgres.yaml` | If company provides managed postgres |
