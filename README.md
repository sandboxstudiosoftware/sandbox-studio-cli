# Sandbox Studio CLI (`sbs`)

[![CI](https://github.com/sandboxstudiosoftware/sandbox-studio-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/sandboxstudiosoftware/sandbox-studio-cli/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/sandboxstudiosoftware/sandbox-studio-cli/badges/coverage.json)](https://github.com/sandboxstudiosoftware/sandbox-studio-cli/actions/workflows/ci.yml)

`sbs` is a cross-platform command-line client for
[Sandbox Studio](https://sandboxstudiosoftware.com). Use it to:

- get AWS credentials for your sandbox accounts, including as a
  `credential_process` provider in `~/.aws/config`,
- open the AWS console for a sandbox in your browser,
- request, approve and manage leases from the terminal or an interactive browser.

> [!IMPORTANT]
> **Requires Sandbox Studio 1.3.1 or later.** The CLI does not work with earlier versions.

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Commands](#commands)
  - [Setup and sign-in](#setup-and-sign-in)
  - [Accessing a sandbox account](#accessing-a-sandbox-account)
  - [Leases](#leases)
  - [Approvals](#approvals)
  - [Other](#other)
  - [Global flags and environment variables](#global-flags-and-environment-variables)
- [Authentication](#authentication)
- [Sessions and credentials](#sessions-and-credentials)
- [How `sbs login` works](#how-sbs-login-works)
- [Development](#development)

## Install

### Requirements

- A Sandbox Studio deployment running **version 1.3.1 or later**. Earlier
  versions don't have the API endpoints the CLI uses for sign-in and
  credentials.
- For browser login (`sbs login`), a deployment that uses Cognito. See
  [Authentication](#authentication).

### Packages

Package managers (planned, not yet published):

```sh
brew install sandboxstudio/tap/sbs      # macOS / Linux
scoop install sbs                        # Windows
```

### From source

Requires Go 1.26 or later:

```sh
go build -o sbs ./cmd/sbs
```

## Quick start

Point the CLI at your deployment and sign in with your browser:

```sh
sbs configure --url https://sandbox.example.com
sbs login
```

Leave the API client ID blank when `configure` asks for it. You only need one
for CI and scripts (see [Authentication](#authentication)).

Then use a lease in whichever way suits you:

```sh
# Pick an Active lease and get a shell with its AWS credentials
sbs assume

# Sign in to the lease's AWS console in your browser
sbs open

# Browse all your leases with the keyboard
sbs lease browse
```

To use a lease as a regular AWS profile, add it to `~/.aws/config`:

```ini
[profile my-lease]
credential_process = sbs creds --lease <lease-id>
region = eu-west-2
```

```sh
aws sts get-caller-identity --profile my-lease
```

The AWS CLI and SDKs call `sbs creds` again whenever the credentials are about
to expire, so the profile keeps working for as long as your Sandbox Studio
session is valid.

### Why not `aws sso login`?

You access a sandbox account through an IAM role that Sandbox Studio creates
for you, not through an IAM Identity Center assignment, so `aws sso login`
cannot get you in. `sbs` fetches temporary credentials from the Sandbox Studio
API instead.

## Commands

Run `sbs <command> --help` for the full list of flags. In the examples below,
`<lease-id>` is a lease UUID, which you can find with `sbs lease list`.

### Setup and sign-in

#### `sbs configure`

Saves the deployment URL and, optionally, an API client ID. The client secret
is always prompted for, never passed as a flag, so it stays out of your shell
history.

```sh
sbs configure --url https://sandbox.example.com
sbs configure --url https://other.example.com --name other   # a second, named context
```

| Flag&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp; | Description |
| --- | --- |
| `--url` | Sandbox Studio URL |
| `--client-id` | API client ID (for CI and scripts) |
| `--name` | Context name (default `default`) |

#### `sbs login`

Opens the Sandbox Studio sign-in page in your browser (email, password and MFA,
or your identity provider) and stores the resulting API session in the OS
keyring.

```sh
sbs login
```

`sbs login --lease <lease-id>` behaves like `sbs creds`, and adding `--console`
behaves like `sbs open`. Those forms exist for compatibility; prefer `creds`
and `open`.

#### `sbs logout`

Removes the API session and cached lease credentials from the OS keyring.

```sh
sbs logout          # keep a stored API client
sbs logout --all    # also remove the API client ID and secret
```

#### `sbs whoami`

Shows the current context, which authentication method is in use (browser
session or API client), the signed-in subject and role, and when the session
expires.

### Accessing a sandbox account

These commands accept `--role ADMIN` to use the shared full-admin role instead
of your own. Only managers and admins can do this, and those credentials last
at most 1 hour.

#### `sbs assume [lease-id]`

Gives you a shell or a command with the lease's AWS credentials. With no lease
ID, an interactive picker lists your Active leases.

```sh
sbs assume                                   # subshell; type `exit` to leave
sbs assume <lease-id> -- aws s3 ls           # run a single command
eval "$(sbs assume --export <lease-id>)"     # set the variables in your current shell
```

By default `sbs assume` writes an AWS profile whose `credential_process` is
`sbs creds`, and sets `AWS_CONFIG_FILE` and `AWS_PROFILE` to point at it, so
the credentials refresh automatically.

| Flag&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp; | Description |
| --- | --- |
| `--export` | Print shell statements for use with `eval` |
| `--shell` | Syntax for `--export`: `auto` (default), `posix`, `fish` or `powershell` |
| `--static` | Export raw `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN` instead. Use this for tools that don't support `credential_process`. These are **not** refreshed. |
| `--role` | `USER` (default) or `ADMIN` |

#### `sbs open [lease-id]`

Signs you in to the lease's AWS account in your default browser. With no lease
ID, it uses the same picker as `sbs assume`. Alias: `sbs console`.

```sh
sbs open
sbs open <lease-id>
sbs open <lease-id> --print    # print the sign-in link instead, e.g. for another browser profile
```

The sign-in link is valid for about 15 minutes. Once you are signed in, the
console session lasts the template's session duration.

#### `sbs creds --lease <lease-id>`

Prints temporary credentials as the JSON object that AWS `credential_process`
expects. Only the JSON goes to stdout; messages go to stderr.

```sh
sbs creds --lease <lease-id>
sbs creds --lease <lease-id> --role ADMIN
```

See [Quick start](#quick-start) for the `~/.aws/config` entry.

### Leases

#### `sbs lease browse`

Opens a full-screen, keyboard-driven view of your leases. Alias: `sbs lease ui`
(or `sbs lease list -i`).

| Key | Action |
| --- | --- |
| `↑` `↓` / `j` `k` | Move |
| `enter` | Actions for the selected lease: details, logs, **Assume role**, **Open AWS console**, suspend, resume, terminate, approve, deny, withdraw |
| `n` | Request a new lease from a template |
| `tab` | Switch between leases and the approvals queue |
| `f` | Change the status filter |
| `r` | Refresh |
| `q` | Quit |

Destructive actions ask for confirmation.

#### `sbs lease list`

Lists your leases. Only Active leases are shown unless you filter.

```sh
sbs lease list
sbs lease list --status Active,Suspended
sbs lease list --all --since 2026-01-01
sbs lease list --user <user-id>            # managers/admins
```

| Flag&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp; | Description |
| --- | --- |
| `--status` | One or more statuses, repeated or comma-separated |
| `--all` | Every status |
| `--since` | Leases started on or after a date (`YYYY-MM-DD` or RFC 3339) |
| `--user` | Another user's leases (managers/admins) |
| `-i`, `--interactive` | Open `sbs lease browse` |

#### `sbs lease get <lease-id>`

Shows the details of a single lease.

#### `sbs template list`

Lists the templates you can request a lease from.

#### `sbs lease request`

Requests a lease from a template, given by ID or exact name. Depending on the
template, the lease starts right away or waits in `PendingApproval`.

```sh
sbs lease request --template "Dev Sandbox" --comments "Workshop prep" --wait
```

| Flag&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp; | Description |
| --- | --- |
| `--template` | Template ID or exact name (required) |
| `--comments` | Comments for the approver |
| `--for-user` | Request on behalf of another user ID |
| `--share-with` | User IDs to share the lease with, repeated or comma-separated |
| `--wait` | Wait up to 10 minutes for the lease to become Active |

#### `sbs lease wait <lease-id>`

Polls a lease until it reaches a status, then exits 0. Exits non-zero if the
lease ends in another final state (denied, setup failed, expired...) or the
timeout passes.

```sh
sbs lease wait <lease-id>
sbs lease wait <lease-id> --for Active --timeout 20m --interval 10s
```

#### Changing a lease's state

```sh
sbs lease withdraw  <lease-id>...   # withdraw your own pending request
sbs lease suspend   <lease-id>...
sbs lease resume    <lease-id>...
sbs lease terminate <lease-id>...   # irreversible: the account is wiped; asks first unless -y/--yes
```

#### Sharing

```sh
sbs lease share   <lease-id> --user <user-id>[,<user-id>...]
sbs lease unshare <lease-id> --user <user-id>[,<user-id>...]
sbs lease team    <lease-id>        # who the lease is shared with
```

#### `sbs lease update <lease-id>` (managers/admins)

Changes a lease's budget and expiry. Only the flags you pass are changed.

```sh
sbs lease update <lease-id> --max-budget 200 --expires 2026-12-31
sbs lease update <lease-id> --no-max-budget
```

| Flag&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp; | Description |
| --- | --- |
| `--max-budget` | Maximum budget in dollars |
| `--no-max-budget` | Remove the budget limit |
| `--expires` | Expiry date (`YYYY-MM-DD` or RFC 3339) |
| `--no-expiry` | Remove the expiry date |
| `--max-duration-hours` | Maximum duration in hours |

#### Logs and secrets

```sh
sbs lease logs    <lease-id>   # account setup logs
sbs lease secrets <lease-id>   # outputs from the template's launch script (may be sensitive)
```

### Approvals

For designated approvers of a template.

```sh
sbs approval list                            # pending requests
sbs approval list --status approved,denied   # or --all
sbs approval approve <lease-id>...
sbs approval deny    <lease-id>...
```

`sbs lease approve` and `sbs lease deny` do the same thing.

### Other

```sh
sbs version                # release builds show the tag; local builds show the version and git SHA
sbs completion zsh         # shell completion script (bash, zsh, fish, powershell)
```

### Global flags and environment variables

| Flag&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp; | Description |
| --- | --- |
| `--context <name>` | Use a named context instead of the current one |
| `--url <url>` | Override the Sandbox Studio URL |
| `-o`, `--output table\|json` | Output format |
| `--debug` | Debug logging to stderr |

| Variable | Description |
| --- | --- |
| `SANDBOX_STUDIO_URL` | Sandbox Studio URL |
| `SANDBOX_STUDIO_CLIENT_ID` | API client ID |
| `SANDBOX_STUDIO_CLIENT_SECRET` | API client secret |
| `SANDBOX_STUDIO_CONFIG_DIR` | Config directory (default: `sandbox-studio` in your OS config directory) |

## Authentication

`sbs` calls the Sandbox Studio API in one of two ways.

**Browser login.** Run `sbs login`; there is no secret to manage. The CLI
exchanges the login for a Sandbox Studio API session and stores only that
session (token, expiry and deployment URL) in the OS keyring. Sessions are not
refreshed. When yours expires, every command, including `credential_process`,
fails with a message telling you to run `sbs login`. The CLI never opens a
browser on its own.

Browser login needs a deployment that uses Cognito. Deployments that use IAM
Identity Center don't offer CLI login, and `sbs login` tells you so.

**API client (client ID and secret).** Use this for CI and scripts. Set it up
with `sbs configure --client-id ...` or with the `SANDBOX_STUDIO_CLIENT_ID` and
`SANDBOX_STUDIO_CLIENT_SECRET` environment variables. A configured API client
takes precedence over a browser session; `sbs whoami` shows which one is in
use.

`sbs logout` removes only the local copies. Session tokens can't be revoked
server-side and expire on their own.

## Sessions and credentials

Sandbox Studio assumes the sandbox role on the server and returns the
credentials to the CLI (`POST /api/leases/{id}/access`). The CLI never talks to
AWS STS or Cognito directly.

- **Duration.** Credentials last the template's session duration, up to 12
  hours. That applies to everyone with access to the lease: the owner, team
  members, and managers or admins using "Login as user".
- **Admin role.** With `--role ADMIN`, managers and admins get the shared
  full-admin role instead. Those credentials last at most 1 hour.
- **Caching.** `sbs creds` caches credentials per lease and role in the OS
  keyring and reuses them until 5 minutes before they expire.
- **First access after a backend upgrade** can take a few seconds while IAM
  changes propagate. The CLI retries 3 times, 3 seconds apart.
- **Console links** from `sbs open` are valid for about 15 minutes. The console
  session itself lasts the template's session duration.

## How `sbs login` works

1. The CLI reads the deployment's public discovery document
   (`GET /api/oauth/.well-known/oauth-authorization-server`) to find the login
   page path and the allowed loopback ports.
2. It generates a throwaway P-256 key pair and a random `state`, listens on the
   first free port on `127.0.0.1` (52900 to 52904 by default), and opens
   `<url>/login?cli_callback=http://127.0.0.1:<port>/callback&cli_state=<state>&cli_key=<public key>`.
3. You sign in on the Sandbox Studio page. The page redirects to
   `http://127.0.0.1:<port>/callback#enc=<ciphertext>&state=<state>`. The token
   is encrypted to the CLI's public key (ECDH P-256, HKDF-SHA256, AES-256-GCM,
   bound to `state`), so another local process that grabs the port sees only
   ciphertext. The loopback page moves the fragment out of the address bar and
   POSTs it back. The CLI checks the Host and Origin headers and the state, then
   decrypts the token with its private key, which never leaves the process.
4. The CLI exchanges the token for an API session
   (`POST /api/auth/token-exchange` with `client: "cli"`) and discards it.

The browser then shows a "you can close this window" page. Its logo is embedded
in the binary, so the page works offline.

## Development

```sh
go test ./...                              # unit tests
go test -cover ./...                       # with coverage per package
go build ./...
goreleaser release --snapshot --clean      # dry-run cross-platform build
```
