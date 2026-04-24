# Tesla OAuth Azure Function

This folder contains a Go-based Azure Functions custom-handler app for Tesla Fleet OAuth and public-key hosting.

## Routes

- `GET /.well-known/appspecific/com.tesla.3p.public-key.pem`
  - Returns the Tesla public key from the `TESLA_PUBLIC_KEY_PEM` application setting.
- `GET /oauth/start`
  - Starts the Tesla authorization-code flow and redirects the browser to Tesla.
- `GET /oauth/callback`
  - Exchanges the authorization code for tokens and stores the refresh token in Azure Key Vault.

## Azure App Settings

At minimum, configure these settings in the Function App:

- `TESLA_CLIENT_ID`
- `TESLA_CLIENT_SECRET`
- `TESLA_REDIRECT_URI`
- `TESLA_REGION`
- `TESLA_SCOPE`
- `TESLA_PUBLIC_KEY_PEM`
- `OAUTH_STATE_HMAC_KEY`
- `KEY_VAULT_URI`
- `TESLA_REFRESH_TOKEN_SECRET_NAME`

Recommended Azure Key Vault references:

- `TESLA_CLIENT_SECRET = @Microsoft.KeyVault(SecretUri=https://<vault>.vault.azure.net/secrets/tesla-client-secret/)`
- `TESLA_PUBLIC_KEY_PEM = @Microsoft.KeyVault(SecretUri=https://<vault>.vault.azure.net/secrets/tesla-public-key-pem/)`
- `OAUTH_STATE_HMAC_KEY = @Microsoft.KeyVault(SecretUri=https://<vault>.vault.azure.net/secrets/oauth-state-hmac-key/)`

`TESLA_PUBLIC_KEY_PEM` is intentionally served from an app setting so the function never needs a local PEM file for the public key.

## Key Vault Permissions

The Function App should use a managed identity.

The identity must be able to:

- resolve Key Vault-backed app settings for `TESLA_CLIENT_SECRET`, `TESLA_PUBLIC_KEY_PEM`, and `OAUTH_STATE_HMAC_KEY`
- create or update the refresh-token secret named by `TESLA_REFRESH_TOKEN_SECRET_NAME`

If you use Key Vault RBAC, the simplest built-in role is `Key Vault Secrets Officer` on the vault. If you use access policies, grant secret `Get`, `List`, and `Set` permissions.

## DNS And Tesla App Registration

Use the custom domain:

- `https://tesla.bellee.net`

Register these Tesla URLs:

- Allowed origin: `https://tesla.bellee.net`
- Redirect URI: `https://tesla.bellee.net/oauth/callback`
- Public key URL: `https://tesla.bellee.net/.well-known/appspecific/com.tesla.3p.public-key.pem`

## Local Development

Key Vault references do not resolve when running locally. Put raw values in `local.settings.json` when testing on your machine.

Example:

```bash
cd function
cp local.settings.json.example local.settings.json
make build
func start
```

Then open:

- `http://localhost:7071/oauth/start`

## Build And Test

```bash
cd function
make test
make build
```

For Azure publish, build the Linux binary first:

```bash
cd function
make build-linux
func azure functionapp publish <function-app-name>
```
