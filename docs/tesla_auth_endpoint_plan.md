# Tesla Auth Endpoint Migration Plan

## Goal
Host Tesla OAuth authorization flow directly in the charging app, with callback at:

- `https://tesla.bellee.net/auth/tesla/callback`

## Scope

1. Add OAuth/public-key endpoints to the main web server
2. Keep dashboard/API endpoints protected by basic auth
3. Persist refresh token to local file on persistent volume
4. Activate refreshed token in-memory without restarting the app
5. Keep Azure Function code in place for now (deprecation later)

## Implementation Steps

### 1. Configuration updates
Add config fields and env vars:

- `TESLA_REDIRECT_URI` (required when not in test mode)
- `TESLA_SCOPE` (optional, default standard Tesla scope set)
- `TESLA_PUBLIC_KEY_PEM_PATH` (required when not in test mode)
- `OAUTH_STATE_HMAC_KEY` (required when not in test mode)
- `TESLA_TOKEN_PATH` (optional, default `/data/tesla-refresh-token`)

Also make `TESLA_REFRESH_TOKEN` optional when not in test mode, because it can be loaded from token file.

### 2. Tesla client runtime token update
Extend `VehicleController` with:

- `SetRefreshToken(ctx context.Context, refreshToken string) error`

Implement in Tesla client:

- update in-memory refresh token
- refresh access token immediately

Implement no-op in test mode client.

### 3. OAuth handlers in app
Create web OAuth handler module with routes:

- `GET /.well-known/appspecific/com.tesla.3p.public-key.pem`
- `GET /auth/tesla`
- `GET /auth/tesla/callback`

Behavior:

- signed HMAC state cookie for CSRF protection
- redirect to Tesla authorize endpoint
- callback validates state
- exchanges code at Tesla token endpoint
- persists refresh token atomically to file
- calls vehicle `SetRefreshToken` to activate immediately

### 4. Route wiring and auth boundary
Register OAuth/public key routes as public (no basic auth), while keeping existing dashboard/API routes protected.

### 5. Startup token bootstrap
On startup, if `TESLA_REFRESH_TOKEN` is empty, read refresh token from `TESLA_TOKEN_PATH` before creating Tesla client.

### 6. Tests
Update/add tests for:

- new config env behavior
- Tesla `SetRefreshToken`
- OAuth handler start/callback/public-key endpoints
- server auth boundary (public OAuth routes + protected APIs)

### 7. Validation
- run `go test ./...`
- verify public key endpoint
- verify auth redirect and callback flow
- verify token file persistence and runtime activation

## Deployment prerequisites

- Tesla app registration:
  - Allowed Origin: `https://tesla.bellee.net`
  - Redirect URI: `https://tesla.bellee.net/auth/tesla/callback`
- Ensure public key endpoint is reachable at:
  - `https://tesla.bellee.net/.well-known/appspecific/com.tesla.3p.public-key.pem`
- Ensure token file path is on persistent Docker volume (`/data`)
