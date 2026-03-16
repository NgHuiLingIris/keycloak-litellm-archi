# Start Services

```
docker compose up -d
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
cp /tmp/samba-cert.pem keycloak-trust/samba-ca.pem
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
| Users DN | `CN=Users,DC=ad,DC=example,DC=com` |

Recommended field values (see knowledge.md for full explanation of each field):

| Field | Value |
|---|---|
| Edit Mode | `READ_ONLY` |
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

In `docker-compose.yml`, under the `litellm` service environment, replace `<YOUR_CLIENT_SECRET>`
with the secret copied from Keycloak:

````
GENERIC_CLIENT_SECRET: "<YOUR_CLIENT_SECRET>"
````

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
http://localhost:4000/sso/login
````

You will be redirected to Keycloak. Log in as a test user:
````
username: alice
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

API master key:
````
sk-1234
````
