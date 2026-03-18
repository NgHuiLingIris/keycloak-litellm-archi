# Kubernetes Setup Guide (minikube)

# Note: If AD does not segregate group, 

This guide deploys the full stack (Samba DC, Postgres, Keycloak, Ollama, LiteLLM) on minikube.

* * *

## Prerequisites

- minikube ≥ 1.32
- kubectl ≥ 1.28
- openssl

* * *

## 1. Start minikube

```bash
minikube start --cpus=4 --memory=8192 --disk-size=30g
```

* * *

## Phase 1 — Namespace, secrets, postgres, samba

### 2. Apply core resources

```bash
kubectl apply -f kubeconfigs/00-namespace.yaml
kubectl apply -f kubeconfigs/01-secrets.yaml
kubectl apply -f kubeconfigs/02-postgres.yaml
kubectl apply -f kubeconfigs/03-samba.yaml
```

### 3. Wait for postgres and samba-dc to be Running

```bash
kubectl get pods -n keycloak -w
```

Expected (both Running before continuing):

```
postgres-<hash>    1/1   Running
samba-dc-<hash>    1/1   Running
```

### 4. Reset samba admin password

The samba-admin-password secret is read at provisioning time. After the pod reaches Running, set it explicitly:

```bash
kubectl exec -n keycloak deployment/samba-dc -- samba-tool user setpassword Administrator --newpassword='Admin1234!'
```

* * *

## TLS Certificate Setup (run once after samba-dc is Running)

Samba auto-generates a self-signed cert with a wrong CN on first start. These steps replace it
with a cert that has `CN=samba-dc` so Keycloak can connect via LDAPS.

### 5. Remove existing certs from samba-dc

```bash
kubectl exec -n keycloak deployment/samba-dc -- sh -c \
  "rm -f /var/lib/samba/private/tls/cert.pem \
         /var/lib/samba/private/tls/key.pem \
         /var/lib/samba/private/tls/ca.pem"
```

### 6. Generate new cert with correct hostname

```bash
openssl req -x509 -newkey rsa:4096 -days 3650 -nodes \
  -keyout /tmp/samba-key.pem \
  -out /tmp/samba-cert.pem \
  -subj "/CN=samba-dc/O=Samba Administration" \
  -addext "subjectAltName=DNS:samba-dc,DNS:samba-dc.keycloak.svc.cluster.local,DNS:localhost"
```

### 7. Copy cert into the samba-dc pod and fix ownership

Get the pod name first:

```bash
SAMBA_POD=$(kubectl get pod -n keycloak -l app=samba-dc -o jsonpath='{.items[0].metadata.name}')
```

Copy and fix permissions:

```bash
kubectl cp /tmp/samba-cert.pem keycloak/$SAMBA_POD:/var/lib/samba/private/tls/cert.pem
kubectl cp /tmp/samba-key.pem  keycloak/$SAMBA_POD:/var/lib/samba/private/tls/key.pem
kubectl cp /tmp/samba-cert.pem keycloak/$SAMBA_POD:/var/lib/samba/private/tls/ca.pem

kubectl exec -n keycloak $SAMBA_POD -- sh -c \
  "chown 0:0 /var/lib/samba/private/tls/*.pem && chmod 600 /var/lib/samba/private/tls/key.pem"
```

### 8. Restart samba-dc to load new cert

Deleting the pod forces a restart (Deployment recreates it):

```bash
kubectl delete pod -n keycloak $SAMBA_POD
```

Wait for the new pod to be Running:

```bash
kubectl get pods -n keycloak -w
```

### 9. Re-apply admin password after restart

```bash
kubectl exec -n keycloak deployment/samba-dc -- samba-tool user setpassword Administrator --newpassword='Admin1234!'
```

### 10. Verify cert CN and SAN

The samba-dc image does not include openssl. Copy the cert to the host and verify locally:

```bash
SAMBA_POD=$(kubectl get pod -n keycloak -l app=samba-dc -o jsonpath='{.items[0].metadata.name}')
kubectl cp keycloak/$SAMBA_POD:/var/lib/samba/private/tls/cert.pem /tmp/samba-verify.pem
openssl x509 -noout -subject -ext subjectAltName -in /tmp/samba-verify.pem
```

Expected output:

```
subject=CN = samba-dc, O = Samba Administration
X509v3 Subject Alternative Name:
    DNS:samba-dc, DNS:samba-dc.keycloak.svc.cluster.local, DNS:localhost
```

### 11. Create keycloak-truststore ConfigMap and enable truststore

```bash
kubectl create configmap keycloak-truststore \
  --from-file=samba-ca.pem=/tmp/samba-cert.pem \
  -n keycloak
```
<!-- 
**Step 11b** — patch `KC_TRUSTSTORE_PATHS` back into the Keycloak deployment now that the cert exists:

```bash
kubectl set env deployment/keycloak \
  KC_TRUSTSTORE_PATHS=/opt/keycloak/truststore/samba-ca.pem \
  -n keycloak
```

This triggers an automatic rollout. Wait for it before proceeding:

```bash
kubectl rollout status deployment/keycloak -n keycloak
``` -->

* * *

## Phase 2 — Keycloak, Ollama, LiteLLM

### 12. Apply remaining services

```bash
kubectl apply -f kubeconfigs/04-keycloak.yaml
kubectl apply -f kubeconfigs/05-ollama.yaml
kubectl apply -f kubeconfigs/06-litellm.yaml
```

### 13. Wait for all pods to be Running

```bash
kubectl get pods -n keycloak -w
```

Expected (all Running before continuing):

```
keycloak-<hash>    1/1   Running
ollama-<hash>      1/1   Running
litellm-<hash>     1/1   Running
```

Keycloak takes ~60s to become ready. Check with:

```bash
kubectl rollout status deployment/keycloak -n keycloak
```

### 14. Set up port-forwards

Open two terminals:

```bash
# Terminal 1 — Keycloak
kubectl port-forward -n keycloak svc/keycloak 8080:8080

# Terminal 2 — LiteLLM
kubectl port-forward -n keycloak svc/litellm 4000:4000
```

Verify:
- Keycloak admin console: http://localhost:8080 (admin / admin)
- LiteLLM health: http://localhost:4000/health/liveliness

* * *

## Configure Keycloak

### 15. Configure LDAP Federation

Navigate to: **User Federation → Add provider → LDAP**

| Field | Value |
|---|---|
| Vendor | Active Directory |
| Connection URL | `ldaps://samba-dc.keycloak.svc.cluster.local:636` |
| Use Truststore SPI | Always |
| Bind type | simple |
| Bind DN | `Administrator@ad.example.com` |
| Bind Credential | `Admin1234!` |

Recommended settings:

| Field | Value |
|---|---|
| Edit Mode | `READ_ONLY` |
| Users DN | `CN=Users,DC=ad,DC=example,DC=com` |
| Username LDAP attribute | `sAMAccountName` |
| RDN LDAP attribute | `cn` |
| UUID LDAP attribute | `objectGUID` |
| User object classes | `person, organizationalPerson, user` |
| Search scope | `Subtree` |
| Read timeout | `10000` |
| Pagination | ON, size `100` |
| Referral | `ignore` |
| Import users | ON |
| Periodic full sync | ON, `86400`s |
| Periodic changed sync | ON, `3600`s |
| Remove invalid users | ON |
| Kerberos | OFF |
| LDAPv3 password modify | ON |
| Validate password policy | OFF |
| Trust email | ON |
| Connection trace | OFF |

### 16. Create LiteLLM OIDC client in Keycloak

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

### 17. Update litellm-sso Secret with the client secret

Replace `<PASTE_SECRET_HERE>` with the secret copied from Keycloak:

```bash
kubectl patch secret litellm-sso -n keycloak \
  --type='json' \
  -p='[{"op":"replace","path":"/data/GENERIC_CLIENT_SECRET","value":"'"$(echo -n '<PASTE_SECRET_HERE>' | base64)"'"}]'
```

Then restart LiteLLM to pick up the new secret:

```bash
kubectl rollout restart deployment/litellm -n keycloak
kubectl rollout status deployment/litellm -n keycloak
```

* * *

## Create AD Test Users

### 18. Create AD test users

The original script uses `docker exec` and won't work inside the pod. Run the `samba-tool` commands directly:

```bash
# Create users
kubectl exec -n keycloak deployment/samba-dc -- samba-tool user create alice Password123! --given-name=alice --surname=Test
kubectl exec -n keycloak deployment/samba-dc -- samba-tool user create bob Password123! --given-name=bob --surname=Test
kubectl exec -n keycloak deployment/samba-dc -- samba-tool user create charlie Password123! --given-name=charlie --surname=Test

# Create group and add members
kubectl exec -n keycloak deployment/samba-dc -- samba-tool group add developers
kubectl exec -n keycloak deployment/samba-dc -- samba-tool group addmembers developers alice
kubectl exec -n keycloak deployment/samba-dc -- samba-tool group addmembers developers bob
```

Verify:

```bash
kubectl exec -n keycloak deployment/samba-dc -- samba-tool user list
```

Expected:

```
Administrator
Guest
alice
bob
charlie
krbtgt
```

Check group members:

```bash
kubectl exec -n keycloak deployment/samba-dc -- samba-tool group listmembers developers
```

* * *

## Pull Ollama Model

### 19. Pull tinyllama

```bash
kubectl exec -n keycloak deployment/ollama -- ollama pull tinyllama
```

* * *

## Test SSO

### 20. Open the SSO login page

```
http://localhost:4000/sso/key/generate
```

You will be redirected to Keycloak. Log in as a test user:

```
username: alice
password: Password123!
```

After login, Keycloak redirects back to LiteLLM.

* * *

## LiteLLM Credentials

UI:

```
http://localhost:4000/ui
username: admin
password: admin123
```

API master key (admin use only):

```
sk-1234
```

* * *

## Group-Based Role Mapping (developers → internal_user)

AD group members of `developers` get the `internal_user` role in LiteLLM (can create API keys).
All other AD users get `internal_user_viewer` (read-only).

### Step 1: Keycloak — LDAP Group Mapper (sync AD groups to Keycloak)

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

### Step 2: Keycloak — Client Group Mapper (include groups in userinfo)

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

### Step 3: LiteLLM — Configure role_mappings via API

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

Then restart LiteLLM:

```bash
kubectl rollout restart deployment/litellm -n keycloak
```

### Step 4: Test

- Log in as alice (in developers group) — should see "Create New Key" button.
- Log in as charlie (not in developers group) — should only see view access.

To fix a user's role immediately without waiting for login:

```bash
kubectl exec -n keycloak deployment/postgres -- \
  psql -U keycloak -d litellm -c \
  "UPDATE \"LiteLLM_UserTable\" SET user_role = 'internal_user_viewer' WHERE user_id = 'charlie';"
```

* * *

## Troubleshooting

### View pod logs

```bash
kubectl logs -n keycloak deployment/samba-dc
kubectl logs -n keycloak deployment/postgres
kubectl logs -n keycloak deployment/keycloak
kubectl logs -n keycloak deployment/ollama
kubectl logs -n keycloak deployment/litellm
```

### Describe a pod (shows events, init container status)

```bash
kubectl describe pod -n keycloak -l app=keycloak
kubectl describe pod -n keycloak -l app=litellm
```

### Exec into a pod

```bash
kubectl exec -it -n keycloak deployment/keycloak -- bash
kubectl exec -it -n keycloak deployment/litellm -- bash
kubectl exec -it -n keycloak deployment/samba-dc -- sh
```

### Refresh TLS truststore (after samba restart)

If samba-dc is restarted and its cert changes:

1. Re-run steps 5–11 (TLS Certificate Setup).
2. Delete and recreate the ConfigMap:

```bash
kubectl delete configmap keycloak-truststore -n keycloak
kubectl create configmap keycloak-truststore \
  --from-file=samba-ca.pem=/tmp/samba-cert.pem \
  -n keycloak
```

3. Restart Keycloak:

```bash
kubectl rollout restart deployment/keycloak -n keycloak
```

Note: The samba admin password is lost on every pod restart. Always re-run:

```bash
kubectl exec -n keycloak deployment/samba-dc -- samba-tool user setpassword Administrator --newpassword='Admin1234!'
```

### Check samba LDAPS connectivity from within the cluster

```bash
kubectl exec -n keycloak deployment/keycloak -- \
  bash -c "echo | openssl s_client -connect samba-dc.keycloak.svc.cluster.local:636 2>/dev/null | openssl x509 -noout -subject"
```

### Teardown

```bash
kubectl delete namespace keycloak
minikube stop
```

To also delete PVC data:

```bash
minikube delete
```
