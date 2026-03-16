# Kubernetes Config — Why It's Written This Way

This document explains the reasoning behind every non-obvious decision in `kubeconfigs/`.
Read this before adapting the manifests to a new environment.

* * *

## Namespaces (`00-namespace.yaml`)

Every resource in these manifests specifies `namespace: keycloak`. A Kubernetes namespace is
a logical boundary — pods in `keycloak` can talk to each other by short DNS names
(`postgres`, `keycloak`, `ollama`) without colliding with identically-named services in other
namespaces on the same cluster.

**Why it matters here:** In a shared company cluster, you will almost certainly be given a
specific namespace (e.g. `devnet-ai`, `platform-tools`). All seven manifests must use the
same namespace, and all cluster-internal DNS names must match:
`<service>.<namespace>.svc.cluster.local`.

* * *

## Secrets (`01-secrets.yaml`)

### `stringData` vs `data`

```yaml
stringData:
  LITELLM_MASTER_KEY: "sk-1234"
```

Kubernetes Secrets have two fields: `data` (base64-encoded values) and `stringData`
(plain text, encoded automatically on apply). `stringData` is used here because it keeps
the YAML human-readable for setup purposes. At rest in etcd, both are stored the same way.

**In production:** These values should come from a secrets manager (Vault, Azure Key Vault,
AWS Secrets Manager) or be sealed with Sealed Secrets / SOPS. Never commit real secrets to git.

### Why the client secret is a separate Secret

`GENERIC_CLIENT_SECRET` (the Keycloak OIDC client secret) cannot be known until after
Keycloak is running and the OIDC client is created. Putting it in a separate `litellm-sso`
Secret means you can patch just that one value and rollout-restart LiteLLM, without touching
anything else.

* * *

## Postgres (`02-postgres.yaml`)

### `defaultMode: 0755` on the ConfigMap volume

```yaml
- name: init-script
  configMap:
    name: pg-init-script
    defaultMode: 0755
```

PostgreSQL's `docker-entrypoint-initdb.d` mechanism runs every `.sh` file in that directory
on first startup. It checks that the file is **executable** (`chmod +x`). A ConfigMap mounted
as a volume defaults to `0644` (read-only for non-owner). Without `defaultMode: 0755`,
postgres silently skips the script and the `litellm` database is never created.

### Why a PersistentVolumeClaim

Postgres data must survive pod restarts. A PVC reserves a volume that outlives the pod.
On minikube the default StorageClass (`standard`) provisions a `hostPath` volume on the node.
On a cloud cluster it provisions a cloud disk (EBS, Azure Disk, GCE PD) automatically.

* * *

## Samba DC (`03-samba.yaml`)

### `automountServiceAccountToken: false`

```yaml
spec:
  automountServiceAccountToken: false
```

By default, Kubernetes injects a service account token into every pod by mounting it at
`/var/run/secrets/kubernetes.io/serviceaccount`. This requires creating that directory path
at container startup. The `instantlinux/samba-dc` image declares a **read-only root
filesystem**, so `runc` cannot create the directory and the container exits 128 before Samba
ever starts. Setting `automountServiceAccountToken: false` tells Kubernetes not to mount the
token at all. Samba has no reason to call the Kubernetes API, so this is safe.

### `securityContext.privileged: true`

Samba Domain Controller requires `CAP_SYS_ADMIN` to manage kernel-level operations
(mounting filesystems, manipulating network interfaces for Kerberos, etc.). `privileged: true`
grants all Linux capabilities. This is a strong privilege — in production, prefer
`capabilities.add: [SYS_ADMIN]` if the image supports it, and ensure the namespace has a
permissive PodSecurityPolicy or PodSecurity label.

### Secret volume with `items[].path`

```yaml
- name: samba-secret
  secret:
    secretName: samba-admin-password
    items:
      - key: samba-admin-password
        path: samba-admin-password
```

The `instantlinux/samba-dc` image reads the admin password from a **file** at
`/run/secrets/samba-admin-password` (Docker Swarm secrets convention). Without `items`,
Kubernetes would mount the secret key as a file named after the key, at the top of the
mount path — which is what we want. The `items` block makes this explicit and readable.

### Two PVCs for samba (`samba-etc`, `samba-lib`)

Docker Compose had two named volumes (`etc` → `/etc/samba`, `lib` → `/var/lib/samba`).
Samba stores its provisioned domain config in `/etc/samba` and its database, keytabs, and
TLS certs in `/var/lib/samba`. Keeping them on separate PVCs mirrors the original design
and allows different retention or size policies per volume.

* * *

## Keycloak (`04-keycloak.yaml`)

### `args` not `command`

```yaml
args: ["start-dev", "--import-realm"]
```

In Kubernetes:
- `command` overrides the container image's **ENTRYPOINT**
- `args` overrides the container image's **CMD** (arguments passed to the entrypoint)

The Keycloak image entrypoint is `/opt/keycloak/bin/kc.sh`. `start-dev` and `--import-realm`
are arguments to that script. Using `command: ["start-dev"]` tries to exec `start-dev` as a
binary directly — it doesn't exist in `$PATH`, causing exit 127.

This is the most common Kubernetes mistake when porting from docker-compose `command:` fields.
In docker-compose, `command:` overrides CMD (arguments). In Kubernetes, `command:` overrides
ENTRYPOINT.

### `KC_HOSTNAME=localhost` and split SSO endpoints

```yaml
KC_HOSTNAME: localhost
KC_HOSTNAME_PORT: "8080"
```

Keycloak embeds the issuer URL inside every token it issues. The issuer is derived from
`KC_HOSTNAME`. If `KC_HOSTNAME` were set to `keycloak.keycloak.svc.cluster.local`, every
token would contain `iss: http://keycloak.keycloak.svc.cluster.local:8080/realms/master`.

When a browser receives that token and LiteLLM tries to validate it by calling the
`/userinfo` endpoint, the issuer in the token must match the URL Keycloak is reachable at.
Setting `KC_HOSTNAME=localhost` means tokens always say `iss: http://localhost:8080/...`,
which matches what the browser sees via port-forward.

This creates a split-endpoint pattern in LiteLLM:

| Env var | URL | Who calls it | Why |
|---|---|---|---|
| `GENERIC_AUTHORIZATION_ENDPOINT` | `http://localhost:8080/...` | **Browser** (redirect) | Must be reachable from the user's machine |
| `GENERIC_TOKEN_ENDPOINT` | `http://keycloak.keycloak.svc.cluster.local:8080/...` | **LiteLLM pod** (server-side) | Must be reachable inside the cluster |
| `GENERIC_USERINFO_ENDPOINT` | `http://keycloak.keycloak.svc.cluster.local:8080/...` | **LiteLLM pod** (server-side) | Same reason |

In a production deployment with a real ingress and TLS, all three would use the same public
hostname (e.g. `https://keycloak.company.com/...`) and the split is unnecessary.

### `initContainer` waiting for postgres

```yaml
initContainers:
  - name: wait-for-postgres
    image: busybox
    command: [sh, -c, "until nc -z postgres.keycloak.svc.cluster.local 5432; ..."]
```

docker-compose has `depends_on`. Kubernetes has no equivalent for readiness — a pod starts
as soon as it's scheduled regardless of whether its dependencies are ready. The `busybox`
init container loops until a TCP connection to postgres on port 5432 succeeds, then exits 0,
allowing the main container to start. This is a lightweight replacement for `depends_on`.

### `readinessProbe` on `/realms/master`

The readiness probe tells Kubernetes when the pod is ready to receive traffic. Keycloak takes
30–90 seconds to start. Without a readiness probe, Kubernetes would mark the pod ready
immediately and LiteLLM's init container would proceed before Keycloak can actually serve
requests. The probe checks that the master realm endpoint responds before the pod is
considered ready.

### `keycloak-truststore` ConfigMap is `optional: true`

```yaml
configMap:
  name: keycloak-truststore
  optional: true
```

This ConfigMap is created during TLS setup (after Samba generates its cert). It cannot be
pre-defined in the YAML because the cert is generated fresh per deployment. `optional: true`
lets Keycloak start before the cert exists — the truststore directory is mounted but empty.
Once the ConfigMap is created and `KC_TRUSTSTORE_PATHS` is set, a rollout restart picks it up.

Without `optional: true`, Kubernetes would refuse to schedule the pod until the ConfigMap
exists, making it impossible to run Keycloak before completing TLS setup.

### `KC_TRUSTSTORE_PATHS` is absent at initial deploy, added after TLS setup

Keycloak hard-fails on startup if `KC_TRUSTSTORE_PATHS` points to a file that doesn't exist.
The file only exists after TLS setup creates the ConfigMap. So the env var is injected via
`kubectl set env` after TLS setup, not pre-declared in the manifest.

* * *

## LiteLLM (`06-litellm.yaml`)

### Config file via ConfigMap + `subPath`

```yaml
volumeMounts:
  - name: config
    mountPath: /app/config.yaml
    subPath: config.yaml
```

Without `subPath`, mounting a ConfigMap at `/app/config.yaml` would replace the entire `/app`
directory with a directory containing only `config.yaml`, destroying the rest of the app.
`subPath` mounts just the single key as a single file at the exact path.

### `STORE_MODEL_IN_DB: "True"`

When this is set, LiteLLM writes model configs and SSO settings to the postgres database.
This allows settings to be updated via API (e.g. role_mappings) without restarting the pod
or editing the config file. It also means the database is the source of truth for runtime
settings — the config file is only the initial seed.

### Why two init containers

LiteLLM depends on both postgres (for its database) and keycloak (for SSO validation at
startup). Two separate init containers run sequentially: first wait for postgres, then wait
for keycloak. Keycloak's init container also waits for postgres, so by the time LiteLLM's
wait-for-keycloak passes, postgres is guaranteed to be up too.

### OIDC client secret in a Secret, not a ConfigMap

`GENERIC_CLIENT_SECRET` is a credential. ConfigMaps are not encrypted at rest and are visible
to anyone with read access to the namespace. Kubernetes Secrets (while also base64-only by
default) are the correct place for credentials and can be restricted via RBAC. In production,
integrate with an external secrets manager.

* * *

## Service Types

| Service | Type | Reason |
|---|---|---|
| postgres | ClusterIP | Internal only — no external access needed or safe |
| samba-dc | ClusterIP | LDAP/LDAPS internal only |
| ollama | ClusterIP | API internal only |
| keycloak | NodePort 30080 | Needs to be reachable from browser for port-forward |
| litellm | NodePort 30000 | Same |

NodePort reserves a fixed port on the node, making `kubectl port-forward` reliable.
In production, use `ClusterIP` for everything and expose via an **Ingress** with TLS.

* * *

## Two-Phase Deployment

Phase 1 deploys postgres and samba first because:
1. The Samba TLS cert must be generated before Keycloak can trust it for LDAPS
2. The cert is generated fresh (not checked into git) — it cannot be pre-loaded into a ConfigMap
3. Keycloak and LiteLLM both depend on postgres being initialized with the correct databases

Phase 2 deploys keycloak, ollama, and litellm after the ConfigMap exists.

In a production environment with pre-existing infrastructure (external postgres, Azure AD),
this two-phase approach is unnecessary — you would deploy only keycloak and litellm.
