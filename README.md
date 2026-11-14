# Sandbox Studio CLI (`sbs`)

A cross-platform command-line client for Sandbox Studio. Log in to your sandbox
accounts from the terminal — including as an AWS `credential_process` provider for
`~/.aws/config` — and drive the Sandbox Studio API.

> **Status: early development.** Requires a Sandbox Studio backend with the
> `cli-enabler-v2` contract.

## Why not `aws sso login`?

Sandbox accounts are accessed through an IAM role that Sandbox Studio creates for
you, not an IAM Identity Center assignment, so `aws sso login` does not work. This
CLI fetches temporary credentials from the Sandbox Studio API instead, and the
AWS CLI refreshes them automatically.

## Install

Planned (not yet published):

```sh
brew install sandboxstudio/tap/sbs      # macOS/Linux
scoop install sbs                        # Windows
```

Build from source (requires Go 1.24+):

```sh
go build -o sbs ./cmd/sbs
```

## Quick start

```sh
# 1. Point the CLI at your deployment. Leave the API client id blank to sign in
#    with your browser; enter one only for CI/scripts (the secret is prompted).
sbs configure --url https://trial.sandboxstudiosoftware.com
sbs login        # browser sign-in; stores an API session in the OS keyring

# 2. Use a lease as an AWS profile in ~/.aws/config:
#    [profile my-lease]
#    credential_process = sbs creds --lease <lease-id>
#    region = ap-southeast-2

aws sts get-caller-identity --profile my-lease

# ...or sign in to its AWS console in your browser:
sbs open <lease-id>        # or just `sbs open` to pick from your Active leases
```

## Commands

| Command | Purpose |
| --- | --- |
| `sbs configure` | Store the deployment URL and, optionally, an API client id/secret (keyring). Leave the client id blank to use browser login. |
| `sbs login` | Browser sign-in (email/password/MFA or your identity provider). Stores a Sandbox Studio API session in the OS keyring. |
| `sbs logout [--all]` | Forget the API session and cached lease credentials. `--all` also removes a stored API client. |
| `sbs login --lease <id> [--console] [--role ADMIN]` | Print credentials, or open the AWS console for a lease. |
| `sbs creds --lease <id> [--role ADMIN]` | Print temporary credentials as `credential_process` JSON. |
| `sbs assume [lease-id] [--export] [--static] [-- cmd...]` | Assume a lease's role in a subshell (default), for one command, or in your current shell via `eval "$(sbs assume --export)"`. Uses an auto-refreshing `credential_process` profile (`AWS_CONFIG_FILE`/`AWS_PROFILE`); `--static` exports raw, non-refreshing keys instead. With no lease id, opens the interactive picker. |
| `sbs open [lease-id] [--role ADMIN] [--print]` | Sign in to a lease's AWS account in your default browser (alias `console`). With no lease id, opens the interactive picker. `--print` prints the sign-in link instead. |
| `sbs whoami` | Show the resolved context, subject, role and session expiry. |
| `sbs lease list [--status Active] [--all] [--user <id>] [--since <date>]` | List leases (all pages). |
| `sbs lease get <id>` | Show a lease. |
| `sbs template list` | Templates you can request a lease from. |
| `sbs lease request --template <id\|name> [--comments ...] [--for-user <id>] [--share-with <ids>]` | Request a lease. |
| `sbs approval list [--status pending] [--all]` | Lease requests you can review. |
| `sbs lease approve\|deny <id>...` | Approve or deny pending requests (also under `approval`). |
| `sbs lease withdraw\|suspend\|resume <id>...` | Withdraw your request, suspend or resume a lease. |
| `sbs lease terminate <id>... [--yes]` | Terminate a lease (asks for confirmation; irreversible). |
| `sbs lease share\|unshare <id> --user <ids>` / `lease team <id>` | Manage and list who a lease is shared with. |
| `sbs lease update <id> [--max-budget N] [--expires D] [--max-duration-hours N]` | Change budget/expiry (managers/admins). |
| `sbs lease logs <id>` / `lease secrets <id>` | Setup logs and build-output secrets. |
| `sbs lease browse` (or `lease list -i`) | Interactive keyboard browser: arrows to move, enter for actions (incl. **Assume role** and **Open AWS console**), `n` new lease, tab for approvals. |
| `sbs lease wait <id> [--for Active] [--timeout 10m]` | Block until a lease reaches a status. |
| `sbs version` | Print the version. Release builds show the tag; local builds show `dev (<git sha>)`. |

Global flags: `--context`, `--url`, `-o/--output table|json`, `--debug`.

CI-friendly env overrides: `SANDBOX_STUDIO_URL`, `SANDBOX_STUDIO_CLIENT_ID`,
`SANDBOX_STUDIO_CLIENT_SECRET`.

## Authentication: browser login vs API client

`sbs` can call the Sandbox Studio API in two ways:

1. **Browser login (no secret to manage).** Run `sbs login`. The CLI exchanges the
   login for a Sandbox Studio API session (`POST /api/auth/token-exchange` with `client: "cli"`) and
   stores only that session (token, expiry, deployment URL) in the OS keyring. There
   is no refresh: when the session expires, every command (including
   `credential_process`) fails with a "run `sbs login`" error, and no browser is
   opened implicitly. `sbs logout` forgets the session and cached lease credentials.
   This only removes local copies; session tokens expire on their own.
2. **API client (client id + secret).** Best for CI. If one is configured (via `configure` or
   `SANDBOX_STUDIO_CLIENT_ID`/`SANDBOX_STUDIO_CLIENT_SECRET`) it **takes precedence** over
   the browser login. `sbs whoami` shows which method is in use.

Browser login needs a Cognito-mode deployment. In IAM Identity Center (IDC) mode
the deployment does not advertise CLI login and `sbs login` says so.

## Sessions and credentials

Sandbox Studio assumes the sandbox role **server-side** and hands the CLI the
result (`POST /api/leases/{id}/access`). The CLI never talks to AWS STS or Cognito
directly.

- **Duration.** Credentials last the lease template's session duration (up to
  12h). That is the same for everyone with access to the lease: the owner, team
  members, and managers/admins using "Login as user". There is no 1-hour fallback.
- **`--role ADMIN`.** Managers and admins can request the shared full-admin role
  instead. Those credentials last at most 1h.
- **Caching.** `sbs creds` caches credentials per lease and role in the OS keyring
  and reuses them until 5 minutes before they expire. When you wire
  `credential_process` into `~/.aws/config`, the AWS CLI and SDKs call `sbs creds`
  again for fresh credentials, so terminal sessions continue for as long as your
  Sandbox Studio session is valid.
- **First access after an upgrade.** The first access to a sandbox after a backend
  upgrade can take a few seconds while IAM propagates. The CLI retries for you
  (3 times, 3s apart).
- **Console.** `sbs open <id>` (also `sbs login --lease <id> --console`, or
  **Open AWS console** in `sbs lease browse`) opens the console link Sandbox
  Studio returns right away, signing you in to the lease's account. The sign-in token inside it is valid for only about
  15 minutes; once you are signed in, the console session lasts the template's
  session duration.

### How `sbs login` works

1. The CLI reads the deployment's public discovery document
   (`GET /api/oauth/.well-known/oauth-authorization-server`) for the login page
   path and the allowed loopback ports.
2. It generates a throwaway P-256 key pair and a random `state`, binds the first
   free loopback port on `127.0.0.1` (52900–52904 by default), and opens
   `<SBS_URL>/login?cli_callback=http://127.0.0.1:<port>/callback&cli_state=<state>&cli_key=<public key>`.
3. You sign in on the Sandbox Studio page (email/password/MFA, or your identity
   provider). The page redirects the browser to
   `http://127.0.0.1:<port>/callback#enc=<ciphertext>&state=<state>`. The token is
   **encrypted to the CLI's public key** (ECDH P-256 + HKDF-SHA256 + AES-256-GCM,
   bound to `state`), so another local process that grabs the loopback port sees
   only ciphertext. The loopback handler serves a tiny page that moves the fragment
   out of the address bar and POSTs it back; the CLI checks Host/Origin and the
   state, then decrypts it with its private key, which never leaves the process.
4. The CLI exchanges the decrypted token for an API session and discards it.

After the login the loopback server shows a branded page with a "you can close this
window" message. The logo is embedded in the binary, so the page works offline.

## Development

```sh
go test ./...        # unit tests
go build ./...
goreleaser release --snapshot --clean   # dry-run cross-platform build
```
