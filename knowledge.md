# Knowledge: Keycloak + LDAP/AD Integration

---

## Stack Overview

| Service    | Role                                                        | Port      |
|------------|-------------------------------------------------------------|-----------|
| Samba DC   | Fake Active Directory (LDAP/AD domain controller)           | 389, 636  |
| PostgreSQL | Database backend for Keycloak                               | —         |
| Keycloak   | Identity/SSO provider; federates users from AD              | 8080      |
| LiteLLM    | LLM proxy/gateway; uses Keycloak for auth/access control    | 4000      |

Samba is a stand-in for a production Azure AD. The integration patterns are the same.

---

## LDAP User Federation in Keycloak

Keycloak's **User Federation** allows it to delegate user storage to an external directory
like Active Directory. Instead of managing users natively in Keycloak, users live in AD and
Keycloak syncs or looks them up on demand.

This means:
- Users log in to Keycloak with their AD credentials
- Keycloak queries AD to verify the password and retrieve user attributes
- Group memberships from AD can be mapped to Keycloak roles

---

## Bind Type / Bind DN / Bind Credentials

These are the credentials Keycloak uses to **log into AD as a service account** — not for
end users, but for Keycloak itself to query the directory (e.g. to look up users, sync them,
validate group membership). Think of it as Keycloak's own AD login that runs in the background.

| Field            | Value                          | Meaning                                  |
|------------------|--------------------------------|------------------------------------------|
| Bind type        | `simple`                       | Username + password authentication       |
| Bind DN          | `Administrator@ad.example.com` | The AD account Keycloak logs in as       |
| Bind credentials | `Admin1234!`                   | Password for that account                |

In production with Azure AD, use a dedicated read-only service account instead of Administrator.

---

## LDAP vs LDAPS vs StartTLS

| Mode      | URL format               | Port | Encryption                          |
|-----------|--------------------------|------|-------------------------------------|
| Plain LDAP | `ldap://host:389`       | 389  | None — rejected by AD by default    |
| LDAPS     | `ldaps://host:636`       | 636  | Implicit TLS from the start         |
| StartTLS  | `ldap://host:389` + flag | 389  | Negotiated after initial connection |

Samba AD (like Azure AD) refuses plain LDAP binds — it requires transport encryption.
Use `ldaps://samba-dc:636` with a trusted CA cert.

---

## TLS Certificate and Truststore

When Keycloak connects to LDAPS, it validates the server's TLS certificate against a trusted
CA. Samba uses a self-signed certificate, so its CA must be explicitly trusted.

**How this is set up:**

1. Samba's CA cert is exported to `keycloak-trust/samba-ca.pem`
2. Keycloak is configured via `KC_TRUSTSTORE_PATHS=/opt/keycloak/truststore/samba-ca.pem`
3. The `keycloak-trust/` directory is mounted into the Keycloak container

The Samba cert CN and SAN must match the hostname Keycloak connects to (`samba-dc`).
If they don't match, you get: `SSLHandshakeException: No name matching samba-dc found`.

**To regenerate the cert with the correct hostname (if needed):**
```
openssl req -x509 -newkey rsa:4096 -days 3650 -nodes \
  -keyout /tmp/samba-key.pem \
  -out /tmp/samba-cert.pem \
  -subj "/CN=samba-dc/O=Samba Administration" \
  -addext "subjectAltName=DNS:samba-dc,DNS:localhost"

docker cp /tmp/samba-cert.pem aicoemaas_samba-dc_1:/var/lib/samba/private/tls/cert.pem
docker cp /tmp/samba-key.pem  aicoemaas_samba-dc_1:/var/lib/samba/private/tls/key.pem
docker cp /tmp/samba-cert.pem aicoemaas_samba-dc_1:/var/lib/samba/private/tls/ca.pem

# Fix ownership (Samba requires root ownership on TLS key)
docker run --rm -v aicoemaas_lib:/var/lib/samba alpine sh -c \
  "chown 0:0 /var/lib/samba/private/tls/*.pem && chmod 600 /var/lib/samba/private/tls/key.pem"

docker restart aicoemaas_samba-dc_1

# Reset admin password after restart
docker exec aicoemaas_samba-dc_1 samba-tool user setpassword Administrator --newpassword='Admin1234!'

# Copy new CA cert to Keycloak truststore
cp /tmp/samba-cert.pem keycloak-trust/samba-ca.pem
docker restart keycloak
```

---

## Keycloak LDAP Connection Settings (Working)

| Field             | Value                                  |
|-------------------|----------------------------------------|
| Vendor            | Active Directory                       |
| Connection URL    | `ldaps://samba-dc:636`                 |
| Use Truststore SPI | Always                                |
| Bind type         | `simple`                               |
| Bind DN           | `Administrator@ad.example.com`         |
| Bind credentials  | `Admin1234!`                           |
| Users DN          | `CN=Users,DC=ad,DC=example,DC=com`     |

---

## Common Errors and Fixes

| Error | Cause | Fix |
|-------|-------|-----|
| `BindSimple: Transport encryption required` | Plain LDAP on port 389, Samba refuses | Use `ldaps://` on port 636 |
| `PKIX path building failed` | Keycloak doesn't trust Samba's CA cert | Add CA cert to `KC_TRUSTSTORE_PATHS` |
| `No name matching samba-dc found` | Cert CN/SAN doesn't include `samba-dc` | Regenerate cert with correct CN/SAN |
| `LDAP connection has been closed` | Connection established but bind dropped | Usually a wrong password or timing issue after container restart — reset password with `samba-tool` |
| `Invalid credentials (49)` | Wrong bind password | Re-run `samba-tool user setpassword` — password resets are lost on container restart |

---

## Important: Password Persistence

The Administrator password set via `samba-tool` is **lost when the Samba container restarts**
because the `secrets.yaml` file content (the Docker secret) is only used during initial
provisioning. Always re-run the password reset command after a restart:

```
docker exec aicoemaas_samba-dc_1 samba-tool user setpassword Administrator --newpassword='Admin1234!'
```

---

## Migrating from Samba DC to Company Active Directory

### First: Know what type of AD your company has

| Type | LDAP supported? | Notes |
|------|----------------|-------|
| On-premises Windows AD | Yes | Standard LDAP/LDAPS on port 389/636 |
| Azure AD DS (Domain Services) | Yes | Managed AD, supports LDAP/LDAPS |
| Azure AD / Entra ID (cloud-only) | No | Must use OIDC/SAML instead of LDAP |

If your company is on a devnet (airgapped), it is almost certainly on-premises Windows AD
or Azure AD DS — both support LDAP and are direct replacements for Samba.

---

### Questions to ask your IT/AD team

**1. What is the LDAP endpoint (hostname or IP)?**
- e.g. `ldaps://dc01.company.com:636`
- There may be multiple domain controllers — ask if there is a load-balanced or
  recommended endpoint for service integrations

**2. What is the domain name (realm)?**
- e.g. `company.com` or `corp.company.com`
- This determines the Users DN: `DC=company,DC=com`

**3. Can you provide a service account for LDAP binding?**
- A read-only account dedicated to Keycloak (do not use a personal or admin account)
- You need: the username in UPN format (`svc-keycloak@company.com`) and its password
- The account only needs read access to the Users OU

**4. What OU (Organisational Unit) are users stored in?**
- e.g. `CN=Users,DC=company,DC=com` (default)
- or a custom OU: `OU=Staff,OU=Accounts,DC=company,DC=com`
- This becomes the **Users DN** in Keycloak

**5. Is LDAPS (port 636) available and required?**
- Ask if plain LDAP (389) is allowed or if LDAPS is enforced
- If LDAPS: ask for the CA certificate or certificate chain used to sign the DC's TLS cert

**6. Are there firewall rules to open?**
- Your Keycloak server needs outbound access to port 636 on the domain controller

---

### What changes in Keycloak when you migrate

Only the LDAP federation settings change. Everything else stays the same.

| Field | Samba (dev) | Company AD (prod) |
|---|---|---|
| Connection URL | `ldaps://samba-dc:636` | `ldaps://dc01.company.com:636` |
| Bind DN | `Administrator@ad.example.com` | `svc-keycloak@company.com` |
| Bind credentials | `Admin1234!` | service account password |
| Users DN | `CN=Users,DC=ad,DC=example,DC=com` | `OU=Staff,DC=company,DC=com` |
| Truststore | self-signed Samba CA cert | corporate CA cert from IT |

For the truststore: get the corporate CA certificate (`.pem` or `.crt`) from IT,
place it in `keycloak-trust/`, and Keycloak will trust it the same way it currently
trusts the Samba self-signed cert.

---

## Keycloak LDAP Federation Field Reference

### General Settings

**Edit Mode**
| Value | Meaning |
|---|---|
| `READ_ONLY` | Keycloak only reads from AD. Recommended for AD — AD is the source of truth. |
| `WRITABLE` | Keycloak can update AD. Requires write permissions on the service account. |
| `UNSYNCED` | Users imported once, then managed in Keycloak only. AD not updated. |

Use `READ_ONLY`.

---

**Users DN**
Base location in AD where Keycloak searches for users.

For this dev setup (realm `AD.EXAMPLE.COM`):
```
CN=Users,DC=ad,DC=example,DC=com
```
The DN is derived by splitting the realm into DC components: `AD` → `DC=ad`, `EXAMPLE` → `DC=example`, `COM` → `DC=com`.

For a custom OU in production:
```
OU=Staff,OU=Accounts,DC=company,DC=com
```

**Relative User Creation DN**
Sub-path under Users DN where new users are created when Edit Mode is `WRITABLE`. Leave blank otherwise.

---

**Username LDAP Attribute**
AD attribute used as the Keycloak username.
- `sAMAccountName` — short login name (e.g. `alice`). Standard for AD. **Use this.**
- `userPrincipalName` — full UPN (e.g. `alice@ad.example.com`). Use if you want UPN logins.

**RDN LDAP Attribute**
Attribute used as the Relative Distinguished Name — unique identifier within a DN.
Use `cn`.

**UUID LDAP Attribute**
Globally unique, immutable identifier per user.
Use `objectGUID` for AD.

**User Object Classes**
AD object classes that identify a user entry.
```
person, organizationalPerson, user
```

**User LDAP Filter**
Optional filter to restrict which AD users Keycloak sees. Leave blank for all users.
Example — only import members of a specific group:
```
(memberOf=CN=developers,CN=Users,DC=ad,DC=example,DC=com)
```

**Search Scope**
| Value | Meaning |
|---|---|
| `One Level` | Only direct children of Users DN |
| `Subtree` | Users DN and all sub-OUs — **use this** |

**Read Timeout**
How long Keycloak waits for LDAP response. Use `10000` (10 seconds).

**Pagination**
Fetches users in pages to avoid timeouts on large directories. Enable, page size `100`.

**Referral**
| Value | Meaning |
|---|---|
| `ignore` | Skip referrals — **recommended**, avoids errors from unreachable AD partitions |
| `follow` | Keycloak follows the referral to another server |

---

### Synchronization Settings

| Setting | Value | Notes |
|---|---|---|
| Import Users | `ON` | Imports AD users into Keycloak on first login or sync |
| Sync Registrations | `OFF` | Only needed if WRITABLE and users self-register |
| Batch Size | `1000` | Users imported per page during full sync |
| Periodic Full Sync | `ON`, `86400`s | Syncs all users once daily |
| Periodic Changed Users Sync | `ON`, `3600`s | Syncs only modified users every hour |
| Remove Invalid Users During Searches | `ON` | Removes Keycloak users no longer found in AD |

---

### Kerberos Integration

**Allow Kerberos Authentication** — SSO for Windows domain-joined machines (no password prompt).
Leave `OFF` unless your environment has a Kerberos KDC configured.

**Use Kerberos for Password Authentication** — use Kerberos instead of LDAP bind to validate passwords.
Leave `OFF` — LDAP simple bind is what is configured here.

---

### Cache Settings

**Cache Policy**
| Value | Meaning |
|---|---|
| `DEFAULT` | Uses realm-level cache settings |
| `EVICT_DAILY` | Clears cache once per day |
| `EVICT_WEEKLY` | Clears cache once per week |
| `MAX_LIFESPAN` | Cache expires after a set time (ms) |
| `NO_CACHE` | Always fetches live from AD (slowest) |

Use `DEFAULT` or `EVICT_DAILY`.

---

### Advanced Settings

| Setting | Value | Notes |
|---|---|---|
| Enable LDAPv3 Password Modify Extended Operation | `ON` | Required for password changes to work correctly with AD |
| Validate Password Policy | `OFF` | Let AD be the authority on password rules |
| Trust Email | `ON` | Email addresses from AD are trusted — no re-verification needed |
| Connection Trace | `OFF` | Logs every LDAP operation. Turn ON only when debugging. |

---

### Recommended Summary for Dev Samba Setup

| Setting | Value |
|---|---|
| Edit Mode | `READ_ONLY` |
| Users DN | `CN=Users,DC=ad,DC=example,DC=com` |
| Username LDAP attribute | `sAMAccountName` |
| RDN LDAP attribute | `cn` |
| UUID LDAP attribute | `objectGUID` |
| User object classes | `person, organizationalPerson, user` |
| Search scope | `Subtree` |
| Read timeout | `10000` |
| Pagination | `ON`, size `100` |
| Referral | `ignore` |
| Import users | `ON` |
| Periodic full sync | `ON`, `86400`s |
| Periodic changed sync | `ON`, `3600`s |
| Remove invalid users | `ON` |
| Kerberos | `OFF` |
| LDAPv3 password modify | `ON` |
| Validate password policy | `OFF` |
| Trust email | `ON` |
| Connection trace | `OFF` |
