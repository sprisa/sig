# Configuration

[← README](../README.md) · [Querying](QUERYING.md) · [Security](SECURITY.md)

Start with one endpoint and the default context. Add named contexts or proxy
headers when your environment needs them.

## Installation

```sh
go install github.com/sprisa/sig@latest
sig version
```

The required Go toolchain is recorded in [`go.mod`](../go.mod). The binary is
installed into `GOBIN` when set, otherwise the `bin` directory under `GOPATH`
(usually `$HOME/go/bin`). If your shell cannot find `sig`, check:

```sh
go env GOBIN GOPATH
```

Add the corresponding binary directory to your shell's `PATH`. For a typical
Unix installation:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

On Windows, add the directory to your user `Path` environment variable. Repeat
the install command to update. For local builds, see
[Contributing](CONTRIBUTING.md#get-started).

## Log in once

Obtain a key for a SigNoz service account with the role needed for your queries,
then connect:

```sh
sig auth login --url https://signoz.example.com
sig auth status
```

Login prompts for the key and checks the service-account identity before saving
it in the OS keychain. Successful authentication does not prove permission to
query every signal; permissions are managed in SigNoz.

For input from a pipe, use `sig auth login --key-stdin`. Noninteractive login
requires that flag or `SIGNOZ_API_KEY`. Query commands never prompt for a key or
open a browser. Interactive login restores terminal settings when finished;
Ctrl-C, Ctrl-D on empty input, SIGINT, and SIGTERM cancel with exit code 5.

```sh
sig auth logout
```

Logout removes the selected context's stored key while preserving its URL.
Environment credentials remain available. See the
[credential lifecycle](SECURITY.md#credentials) for storage and revocation details.

## Headless and CI usage

Have your secret manager or execution environment supply these variables:

| Variable | Purpose |
| --- | --- |
| `SIGNOZ_URL` | Reachable SigNoz API URL |
| `SIGNOZ_API_KEY` | Service-account key; overrides the selected context's stored key |
| `SIGNOZ_CUSTOM_HEADERS` | Optional extra headers for reverse-proxy authentication |
| `SIG_CONFIG_DIR` | Optional override for the local configuration directory |

Then run commands directly, without login:

```sh
# SIGNOZ_URL and SIGNOZ_API_KEY are already supplied by the environment.
sig auth status
sig logs search --since 15m
```

With an environment URL and key, querying does not write configuration or access
the keychain. These are also the URL/key variable names used by the official MCP
server, but sig connects directly to the SigNoz API, not the MCP endpoint.

## Multiple contexts

A context remembers an endpoint and its credential reference. Most users can
stay with `default`; use names when working with multiple environments:

```sh
sig auth login --context staging --url https://staging.example.com
sig --context staging logs search --since 15m

sig config get-contexts
sig config current-context
sig config use-context staging
sig config delete-context old-instance
```

The first login selects its context. Additional named logins do not change the
current context. Login without `--context` updates the current context. To delete
the current context while others exist, select another first. Deleting the last
context returns to an unconfigured `default`.

### Resolution order

1. `--context` selects a context instead of the configured current one.
2. `SIGNOZ_URL` overrides that context's endpoint for the invocation.
3. `SIGNOZ_API_KEY` overrides its stored key without consulting the keychain.
4. A changed endpoint requires an environment key too; a stored key is not reused
   for a different URL.

During login, `--url` wins over `SIGNOZ_URL`, then the context URL.
`--key-stdin` wins over `SIGNOZ_API_KEY`, then the interactive prompt.

## Reverse-proxy headers

`SIGNOZ_CUSTOM_HEADERS` uses the official MCP server's comma-separated
`Name:Value,Name:Value` format:

```sh
# Syntax example; supply real values through your environment.
export SIGNOZ_CUSTOM_HEADERS='CF-Access-Client-Id:example.access,CF-Access-Client-Secret:<proxy-secret>'
sig auth status
sig logs search --since 15m
```

- Entries split at commas and the first colon in each entry. Surrounding
  whitespace is trimmed; colons within values are preserved.
- Empty values are allowed. Commas inside values are not representable: there is
  no quoting or escape syntax.
- `Authorization:Bearer <proxy-token>` and `Cookie:session=<value>` are accepted.
  They supplement the service-account key.
- Headers apply to every API request, including login, status, and each page.
  They work with either an environment key or a stored key.
- They apply to the effective endpoint for the current invocation. Change or
  unset them when switching endpoints or contexts. They are never persisted.

Malformed entries, invalid HTTP names/control characters, and case-insensitive
duplicates produce a usage error (exit 2), rather than being silently skipped.
The environment value is limited to 64 KiB and 64 headers. Offline commands do
not parse or require it. See [reserved headers](SECURITY.md#custom-header-boundaries)
for the fields the CLI owns.

Set `SIGNOZ_URL` to your backend or reverse-proxy URL. The MCP gateway's
`X-SigNoz-URL` header is not used. Networking, forward proxies, and temporary
port-forwards are operator-managed. See [transport requirements](SECURITY.md#transport).

## Configuration location

Configuration is `sig/config.json` under Go's `os.UserConfigDir()`:

| Platform | Typical path |
| --- | --- |
| macOS | `~/Library/Application Support/sig/config.json` |
| Linux | `$XDG_CONFIG_HOME/sig/config.json` or `~/.config/sig/config.json` |
| Windows | `%AppData%\sig\config.json` |

Set `SIG_CONFIG_DIR` to use another directory.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `sig` is not found | Add the Go binary directory to `PATH` |
| Keychain access fails | Unlock the keychain, or supply an environment key; Linux needs Secret Service and D-Bus for stored credentials |
| Endpoint change is rejected | Supply `SIGNOZ_API_KEY` with the new URL or log in to that endpoint |
| Authentication fails or the response is HTML | Check the URL, service-account key, proxy access, and custom headers |
| Authentication succeeds but a query is denied | Check the service account's SigNoz role |
| Credential cleanup fails after a local change | Check `auth status` and `config get-contexts`, repair keychain access, and retry the failed operation |

For timeout, result-size, and empty-data issues, see
[query troubleshooting](QUERYING.md#troubleshooting). Contributor-level details
of credential cleanup are in [Contributing](CONTRIBUTING.md#configuration-mutations).
