# Start Services

First-time AD provisioning only:
```
export SAMBA_DOMAIN_ACTION=provision (edit from DOMAIN_ACTION)
```

Prepare the keycloak-trust cert first.

Normal starts (after the domain already exists) should use the default `SAMBA_DOMAIN_ACTION=start`
to avoid repeated provisioning/DNS update noise.

```
docker compose up -d
```

* * *

# NeMo Guardrails

This repo includes a NeMo Guardrails sidecar (`nemo-guardrails`) wired to LiteLLM in two ways:

- `tinyllama_guardrails` routes the whole model call through NeMo Guardrails.
- `nemo-guardrails` is registered in LiteLLM's Guardrails tab through the top-level `guardrails:` config.

Start or rebuild the sidecar:
```bash
docker compose up -d --build nemo-guardrails litellm
```

NeMo Guardrails talks to LiteLLM through its OpenAI-compatible API, so the `nemo-guardrails`
container also needs `OPENAI_API_KEY` set to the LiteLLM master key.

Test NeMo Guardrails directly:
```bash
curl -s http://localhost:8000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"tinyllama","messages":[{"role":"user","content":"Which party should I vote for?"}]}' | jq
```

Test the model route through LiteLLM:
```bash
curl -s http://localhost:4000/v1/chat/completions \
  -H 'Authorization: Bearer sk-1234' \
  -H 'Content-Type: application/json' \
  -d '{"model":"tinyllama_guardrails","messages":[{"role":"user","content":"Which stock should I invest in?"}]}' | jq
```

Test the LiteLLM Guardrails tab entry:
```bash
curl -s http://localhost:4000/v1/chat/completions \
  -H 'Authorization: Bearer sk-1234' \
  -H 'Content-Type: application/json' \
  -d '{"model":"tinyllama","messages":[{"role":"user","content":"Which party should I vote for?"}],"guardrails":["nemo-guardrails"]}' | jq
```

* * *

# Run Test User Population in Samba

```
chmod +x scripts/create_ad_test_users.sh
./scripts/create_ad_test_users.sh
```

* * *

# Verify Active Directory Is Running

Enter the Samba container (no bash — use sh):
````
docker exec -it aicoemaas_samba-dc_1 sh
````

Run:
````
samba-tool user list
````

Expected results:
````
Administrator
Guest
alice
bob
charlie
krbtgt
````

Check group members:
````
samba-tool group listmembers developers
````

* * *

# TLS Certificate Setup (run once after first docker compose up)

Samba generates a self-signed cert with the wrong CN on first start. These commands replace it
with a cert that has CN=samba-dc so Keycloak can connect via LDAPS.

Run from the project root:

````
# 1. Remove existing certs
rm -f keycloak-trust/samba-ca.pem
docker run --rm -v aicoemaas_lib:/var/lib/samba alpine sh -c \
  "rm -f /var/lib/samba/private/tls/cert.pem \
         /var/lib/samba/private/tls/key.pem \
         /var/lib/samba/private/tls/ca.pem"

# 2. Generate new cert with correct hostname
openssl req -x509 -newkey rsa:4096 -days 3650 -nodes \
  -keyout /tmp/samba-key.pem \
  -out /tmp/samba-cert.pem \
  -subj "/CN=samba-dc/O=Samba Administration" \
  -addext "subjectAltName=DNS:samba-dc,DNS:localhost"

# 3. Copy cert into Samba and fix ownership
docker cp /tmp/samba-cert.pem aicoemaas_samba-dc_1:/var/lib/samba/private/tls/cert.pem
docker cp /tmp/samba-key.pem  aicoemaas_samba-dc_1:/var/lib/samba/private/tls/key.pem
docker cp /tmp/samba-cert.pem aicoemaas_samba-dc_1:/var/lib/samba/private/tls/ca.pem

docker run --rm -v aicoemaas_lib:/var/lib/samba alpine sh -c \
  "chown 0:0 /var/lib/samba/private/tls/*.pem && chmod 600 /var/lib/samba/private/tls/key.pem"

# 4. Restart Samba and reset the admin password
docker restart aicoemaas_samba-dc_1
sleep 5
docker exec aicoemaas_samba-dc_1 samba-tool user setpassword Administrator --newpassword='Admin1234!'

# 5. Copy CA cert to Keycloak truststore and restart Keycloak
sudo cp /tmp/samba-cert.pem keycloak-trust/samba-ca.pem
docker compose up -d keycloak
sleep 15

# 6. Verify cert
echo | openssl s_client -connect localhost:636 2>/dev/null | openssl x509 -noout -subject -ext subjectAltName
````

Expected output:
````
subject=CN = samba-dc, O = Samba Administration
X509v3 Subject Alternative Name:
    DNS:samba-dc, DNS:localhost
````

Note: The admin password set via samba-tool is lost on container restart. Always re-run step 4
after restarting the Samba container.

* * *

# Open Keycloak

Open browser:
````
http://localhost:8080
````

Login:
````
username: admin
password: admin
````

* * *

# Configure LDAP Federation in Keycloak

Navigate to:
````
User Federation → Add provider → LDAP
````

LDAP Connection Settings:

| Field | Value |
|---|---|
| Vendor | Active Directory |
| Connection URL | `ldaps://samba-dc:636` |
| Use Truststore SPI | Always |
| Bind type | simple |
| Bind DN | `Administrator@ad.example.com` |
| Bind Credential | `Admin1234!` |

Recommended field values (see knowledge.md for full explanation of each field):

| Field | Value |
|---|---|
| Edit Mode | `READ_ONLY` |
| Users DN | `CN=Users,DC=ad,DC=example,DC=com` |
| Username LDAP attribute | `sAMAccountName` — not `cn`, wrong value causes login failure |
| RDN LDAP attribute | `cn` |
| UUID LDAP attribute | `objectGUID` |
| User object classes | `person, organizationalPerson, user` |
| Search scope | `Subtree` |
| Read timeout | `10000` |
| Pagination | ON, size `100` |
| Referral | `ignore` — critical: `follow` causes `user_not_found` as Docker cannot resolve AD referral hostnames |
| Import users | ON |
| Periodic full sync | ON, `86400`s |
| Periodic changed sync | ON, `3600`s |
| Remove invalid users | ON |
| Kerberos | OFF |
| LDAPv3 password modify | ON |
| Validate password policy | OFF |
| Trust email | ON |
| Connection trace | OFF |

* * *

# Create LiteLLM OIDC Client in Keycloak

This enables SSO redirect: LiteLLM → Keycloak (AD login) → back to LiteLLM.

1. Go to **Clients → Create client**
2. Client type: `OpenID Connect`
3. Client ID: `litellm`
4. Click **Next**
5. Turn **Client authentication** ON
6. Turn **Standard Flow** ON
7. Turn **Direct Access Grants** OFF
8. Turn **Service Account Roles** ON
9. Click **Next → Save**
10. Fill in the URL fields:

| Field | Value |
|---|---|
| Root URL | `http://localhost:4000` |
| Home URL | `http://localhost:4000` |
| Valid redirect URIs | `http://localhost:4000/*` |
| Valid post logout redirect URIs | `http://localhost:4000/*` |
| Web origins | `http://localhost:4000` |

11. Click **Save**
12. Go to **Credentials** tab → copy the **Client secret**

* * *

# Configure LiteLLM SSO (docker-compose.yml)

Open `docker-compose.yml` and update `GENERIC_CLIENT_SECRET` under the `litellm` service with
the client secret copied from Keycloak's **Credentials** tab. The file already has a value here
— replace it with your newly generated secret:

````
GENERIC_CLIENT_SECRET: "<paste secret from Keycloak Credentials tab>"
````

If the secret in `docker-compose.yml` does not match Keycloak, every login attempt returns
Internal Server Error (`invalid_client_credentials`). This must be updated every time the
Keycloak client is recreated.

The token and userinfo endpoints must use `host.docker.internal` (not `keycloak`) to avoid
a token issuer mismatch. Keycloak issues tokens with issuer `http://localhost:8080/...` (what
the browser sees), but if LiteLLM calls `keycloak:8080` for userinfo, Keycloak expects issuer
`http://keycloak:8080/...` — mismatch causes Internal Server Error after login.

`KC_HOSTNAME: localhost` in the Keycloak service and `host.docker.internal` in the LiteLLM
endpoints ensure the issuer always matches. This is already set in `docker-compose.yml`.

Then restart:
````
docker compose up -d keycloak litellm
````

* * *

# Pull Ollama Model

````
docker exec ollama ollama pull tinyllama
````

* * *

# Test SSO

Open:
````
http://localhost:4000/sso/key/generate
````

You will be redirected to Keycloak. Log in as a test user:
````
username: freddy
password: Password123!
````

After login, Keycloak redirects back to LiteLLM.

* * *

# LiteLLM Credentials

UI:
````
http://localhost:4000/ui
username: admin
password: admin123
````

API master key (admin use only):
````
sk-1234
````

* * *

# LiteLLM API Keys for SSO Users

SSO users (e.g. alice) log in via Keycloak and are assigned the `internal_user` role,
which allows them to generate their own personal API keys.

To generate a key as alice:
1. Log in via SSO at `http://localhost:4000/ui/login`
2. Navigate to **API Keys → Create New Key**
3. Use the generated key for API calls:

````
curl http://localhost:4000/v1/chat/completions \
  -H "Authorization: Bearer <alice-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model": "tinyllama", "messages": [{"role": "user", "content": "hello"}]}'
````

If the Create Key button is missing, the user's role is `internal_user_viewer`.
This is controlled by AD group membership (see Group-Based Role Mapping section below).

* * *

# Group-Based Role Mapping (developers → internal_user)

AD group members of `developers` get `internal_user` role in LiteLLM (can create API keys).
All other AD users get `internal_user_viewer` (read-only).

Roles are re-evaluated on **every login** — changing group membership in AD takes effect on next login.

If there is segregation, continue and update `litellm_config.yaml` with:
```yaml
litellm_settings:
  default_internal_user_params:
    user_role: "internal_user_viewer"
```
Then restart: `docker compose up -d litellm`

## Step 1: Keycloak — LDAP Group Mapper (sync AD groups to Keycloak)

Navigate to: **User Federation → LDAP → Mappers → Add mapper**

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

Click **Save**, then click **Sync LDAP Groups to Keycloak**.

## Step 2: Keycloak — Client Group Mapper (include groups in userinfo)

Navigate to: **Clients → litellm → Client Scopes → litellm-dedicated → Mappers → Configure a new mapper**

| Field | Value |
|---|---|
| Mapper Type | `Group Membership` |
| Name | `litellm-groups` |
| Token Claim Name | `litellm-groups` |
| Full group path | OFF |
| Add to ID token | ON |
| Add to access token | ON |
| Add to userinfo | ON |

Click **Save**.

## Step 3: LiteLLM — Configure role_mappings via API

`STORE_MODEL_IN_DB: "True"` must be set in `docker-compose.yml` under the `litellm` service
(already set). Restart LiteLLM if you just added it:
````
docker compose up -d litellm
````

Then call the SSO settings API with the master key:
````
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
````
After runnign this curl, you would need to restart LiteLLM.

This saves the role_mappings to the `LiteLLM_SSOConfig` table in the database.
Re-run this command any time you need to change the group-to-role mapping.

## Step 4: Test

Log in as alice (in developers group) — should see "Create New Key" button.
Log in as charlie (not in developers group) — should only see view access.

Note: Existing users with wrong roles will be corrected automatically on next login.
To fix immediately without waiting for login:
````
docker exec keycloak-db psql -U keycloak -d litellm -c \
  "UPDATE \"LiteLLM_UserTable\" SET user_role = 'internal_user_viewer' WHERE user_id = 'charlie';"
````

# Adding Models to LiteLLM

Edit `litellm_config.yaml` to add models, then restart:

````
docker compose up -d litellm
````

Pull new Ollama models first:
````
docker exec ollama ollama pull <model-name>
````
