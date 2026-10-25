# 0013. Sign-in for kubexray on the internet

- Status: Accepted
- Date: 2026-10-25

## Context

In-cluster kubexray (ADR-0010) authenticates with one shared token, or trusts an auth proxy the operator runs. That works for port-forwarding, but a team that puts kubexray behind an Ingress needs what every internal tool has: people sign in as themselves, access follows the company's directory, it can be withdrawn per person, and there's a record of who looked. Running oauth2-proxy beside kubexray is a burden many won't take on.

## Decision

### 1. OpenID Connect, built in

`kubexray serve --auth=oidc` signs people in with any OpenID Connect provider: Google, Okta, Microsoft Entra ID (Azure AD), Keycloak, Dex, Auth0. kubexray is a confidential client using the authorization code flow with PKCE, a `state` value against CSRF, and a `nonce` bound to the ID token. The ID token is verified with the provider's published keys (issuer, audience, expiry, nonce). kubexray never sees a password. GitHub, which isn't an OIDC provider, gets its own small adapter later.

Configuration, all from Helm values: the issuer URL, the client ID, the client secret (from a Secret), extra scopes, and the claim that carries groups.

### 2. Who gets in

Signing in proves who someone is; an allow-list decides whether they get in. At least one rule is required. There is no "anyone with an account at the provider" mode, which for Google would mean anyone on the internet.

- `allow.emails`: exact addresses
- `allow.domains`: everyone whose verified email is in a domain
- `allow.groups`: members of a group, from the ID token's groups claim (Okta, Entra ID and Keycloak can include it)

Email and domain rules use the email only when the provider says it's verified (`email_verified`), because some providers let people put any address on their account. Group rules don't need an email, which is how Microsoft Entra ID (no `email_verified`) is used. Everyone who gets in has the same read-only access: kubexray never changes the cluster, so there are no roles.

### 3. Sessions

After sign-in kubexray sets a session cookie: the person's email and name and an expiry (default 12 hours), signed with HMAC-SHA256 under a key from a Secret, so sessions survive restarts and any replica can check them. It is HttpOnly and Secure. It is `SameSite=Lax`, because the provider redirects back from another site and a Strict cookie wouldn't be sent there. Lax still keeps the cookie off cross-site subresource requests, every API call is read-only, and WebSockets keep their Origin check. Signing out clears it in that browser. Sessions are stateless, so a copied cookie stays valid until it expires; live streams close when the session they were opened with ends. A changed key signs everyone out.

### 4. Machines keep the token

Alertmanager (ADR-0011) and scripts can't click "sign in". With `--auth=oidc`, Bearer tokens from the token Secret are still accepted, so they keep working.

### 5. Abuse and audit

Bad bearer tokens are rate limited per client address. Sign-in callbacks aren't: codes are single-use, bound to the PKCE verifier and checked by the provider, and behind an Ingress every client shares one address, so a per-address limit would let anyone lock everyone out. Audit events are logged as structured JSON with `"audit": true`: sign-in (who, from where), access denied (who, why), sign-out, and replay exports (who, which window, anonymized or not), so a log pipeline can keep and search them.

### 6. The browser side

Unauthenticated page loads redirect to the provider; API calls get 401. After sign-in, people return to the page they asked for (relative paths only, never another site). The header shows who is signed in, with Sign out.

## Consequences

- A team signs in with the accounts it already has, and leavers lose access when their account goes.
- Operators register kubexray with their provider once (a redirect URL of `https://<host>/auth/callback`) and set a few Helm values.
- kubexray now holds an OAuth client secret and a session key; both come from Secrets and are never logged.
