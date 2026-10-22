# Gmail TUI

`gmail-tui` is a keyboard-driven terminal client for Gmail, written in Go on top of Bubble Tea's Model-View-Update framework. It talks to the Gmail API directly over OAuth2 (no IMAP/SMTP), and handles inbox browsing, search, labels, compose/reply, attachment transfer, and read/unread state entirely from the terminal. The project is structured as a small two-package system — a Gmail API client (`api/`) and a terminal UI (`ui/`) — with unit, property-based, and integration tests covering both.

## Features

- Concurrent inbox fetch using bounded worker goroutines, backed by an LRU cache to avoid redundant API calls.
- Compose, reply (with `Re:` subject normalization and thread headers), delete, search, label browsing, read/unread toggling.
- Attachment download with filename sanitization, symlink-safe exclusive writes, and numbered names instead of overwrites.
- Layered configuration: built-in defaults → TOML file → `GMAIL_TUI_*` env vars.
- Structured JSON logging to `~/.config/gmail-tui/app.log`.
- Pagination and preserved search state across navigation.

## Architecture

Bubble Tea enforces a strict Model-View-Update loop: user input and async API results become messages, `Update` produces a new model plus any follow-up commands, `View` renders it. The API package is kept UI-agnostic — it returns plain data/errors, and `ui/commands.go` wraps calls as `tea.Cmd`s so the network never blocks the render loop.

```
┌──────────────────────────────┐
│            main.go            │
│  load config → init logger    │
│  → init Gmail client → run TUI│
└───────────────┬────────────────┘
                │
        ┌───────┴────────┐
        ▼                 ▼
┌───────────────┐   ┌─────────────────────────────┐
│     api/       │   │            ui/               │
│ OAuth, Gmail   │◄──┤ Model-View-Update (Bubble Tea)│
│ REST calls,    │   │  model.go   - app state        │
│ parsing,       │   │  update.go  - msg handling      │
│ validation,    │   │  commands.go- async tea.Cmd     │
│ LRU cache,     │   │  view.go    - rendering          │
│ JSON logger    │   │  keys.go    - keybindings        │
└───────┬────────┘   └─────────────────────────────┘
        │
        ▼
  Gmail REST API (google.golang.org/api/gmail/v1)
```

Async operations (fetch, send, download) run as `tea.Cmd`s returning a result message; `Update` never calls the network synchronously.

## Tech Stack

- **Language:** Go 1.26.10
- **TUI framework:** `charmbracelet/bubbletea`, `bubbles`, `lipgloss`
- **Gmail access:** `google.golang.org/api` (Gmail v1), `golang.org/x/oauth2`
- **Config:** `BurntSushi/toml`
- **Testing:** stdlib `testing`, `pgregory.net/rapid` (property-based tests, vendored under `third_party/rapid`)
- **Tooling:** `golangci-lint`, `pre-commit` (gofmt, goimports, go vet), GitHub Actions CI

## Installation

Prerequisites: Go 1.26.10+, a Google account with Gmail enabled, and a Google Cloud project with the Gmail API enabled.

```bash
git clone https://github.com/rdx40/gmail-tui
cd gmail-tui
go build .
```

Create an OAuth 2.0 Desktop App client in Google Cloud Console, download it as `credentials.json`, and place it in the project root (or point `GMAIL_TUI_CREDENTIALS` at it).

## Configuration

Resolved in order: built-in defaults → `~/.config/gmail-tui/config.toml` → `GMAIL_TUI_*` env vars.

```toml
max_results = 10
search_max_results = 30
downloads_dir = "downloads"
max_concurrent = 5
cache_max_size = 500
log_level = "INFO"
inbox_query = "in:inbox category:primary"
```

Equivalent env vars: `GMAIL_TUI_MAX_RESULTS`, `GMAIL_TUI_SEARCH_MAX_RESULTS`, `GMAIL_TUI_DOWNLOADS_DIR`, `GMAIL_TUI_MAX_CONCURRENT`, `GMAIL_TUI_CACHE_MAX_SIZE`, `GMAIL_TUI_LOG_LEVEL`, `GMAIL_TUI_INBOX_QUERY`, `GMAIL_TUI_CREDENTIALS`. `GMAIL_TUI_READ_ONLY=1` is also read from the environment (not from the TOML file); see [OAuth, storage, and privacy](#oauth-storage-and-privacy).

`inbox_query` controls what the main list shows — e.g. `in:inbox` to include all categories, not just Primary.

## Running locally

```bash
go run .
```

On first launch the app opens your browser for OAuth authorization and captures the redirect on a local loopback port — no copy-paste needed. If a local listener can't be started (headless environments), it falls back to printing the URL and reading the code from the terminal. The token is cached locally after that.

For day-to-day verification:

```bash
go test ./...
go test -tags integration ./api/...
go build ./...
go vet ./...
golangci-lint run
```

(`make test`, `make build`, `make lint`, `make coverage` wrap the same commands if `make` is available.)

Running the `aloop` gates requires `rtk`, `make`, `golangci-lint` v2.10.1 (pinned to match CI), and `govulncheck` on `PATH`, in addition to Go. Install the two Go tools with:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1
go install golang.org/x/vuln/cmd/govulncheck@latest
```

Add `$(go env GOPATH)/bin` to `PATH` (or your `GOBIN` if set).

## Example usage

| Key | Action |
| --- | --- |
| `j` / `k` | Move selection |
| `enter` | Open selected email |
| `/` | Search |
| `c` | Compose |
| `r` | Reply |
| `d` | Move to trash |
| `a` | Archive (remove from inbox) |
| `m` | Toggle read/unread |
| `l` | Labels |
| `R` | Refresh inbox |
| `ctrl+d` | Download attachment |
| `n` / `p` | Next / previous page |
| `?` | Help |
| `q` | Quit |

Sending with an empty recipient is rejected inline (`Error: recipient address is required`); an empty subject prompts `Send with no subject? [y/n]` rather than failing silently.

## Project Structure

```
.
├── main.go            # composition root: config → logger → client → tea.Program
├── api/                # Gmail API layer, no UI dependency
│   ├── auth.go          # OAuth2 flow, token persistence
│   ├── client.go         # Gmail REST calls (list, get, send, trash, labels, attachments)
│   ├── attachment_transport.go # caps attachment HTTP responses before JSON decoding
│   ├── cache.go          # bounded LRU cache for fetched messages
│   ├── config.go         # layered config load (defaults/TOML/env)
│   ├── parse.go           # MIME/body parsing, HTML stripping
│   ├── validate.go        # address/attachment/size validation
│   ├── errors.go           # typed error values used across the package
│   ├── logger.go            # structured JSON logger
│   ├── types.go              # shared data types (Message, Attachment, ...)
│   ├── interfaces.go          # client interface for UI-side mocking
│   └── *_test.go, property_test.go, integration_test.go
├── ui/                 # Bubble Tea TUI layer
│   ├── model.go          # application state
│   ├── update.go          # message handling / state transitions
│   ├── commands.go         # async tea.Cmd wrappers around api/ calls
│   ├── view.go              # rendering
│   ├── sanitize.go          # strips terminal control sequences from untrusted text
│   ├── keys.go               # keybinding definitions
│   └── *_test.go, property_test.go
├── third_party/rapid/  # vendored pgregory.net/rapid (property-based testing)
├── images/             # screenshots used in this README
├── .github/workflows/  # CI: build, test, lint
├── loop.config.mjs, .loop/prompts/  # aloop phase configuration and tracked prompts
└── .golangci.yml, .pre-commit-config.yaml
```

## Design Decisions

- **Bubble Tea / MVU over ad-hoc terminal rendering:** a strict message-driven update loop keeps state transitions testable (`update_test.go`) without a running terminal, and makes async Gmail calls composable as `tea.Cmd` instead of littering goroutines through the render path.
- **`api/` has zero UI imports:** the Gmail client is usable standalone and unit-testable without spinning up Bubble Tea. `interfaces.go` exists specifically so `ui/` can mock the client in tests.
- **LRU cache in front of Gmail REST calls:** Gmail API quota and latency make repeated identical fetches (e.g., re-rendering the same inbox page) expensive; a bounded in-memory cache trades a small memory cost for fewer round trips.
- **Layered config (defaults → TOML → env):** covers both the "just run it" case and containerized/CI use where env vars are the natural override mechanism, without forcing either.
- **Capabilities follow the granted scopes:** compose/reply accept send, compose, modify, or full-mail grants; trash/archive/read-toggle require modify or full-mail grants. Unknown grants fail closed. `GMAIL_TUI_READ_ONLY=1` also blocks these actions locally. See the OAuth and privacy notes below.

## Challenges

- Gmail messages arrive as nested multipart MIME with inconsistent charset/encoding; `parse.go` and its large test suite (`parse_test.go`, `property_test.go`) exist because plain-text extraction and HTML stripping had many edge cases (empty parts, base64 padding, nested alternatives).
- Attachment handling needed explicit path-escape and size-limit checks (`sanitizeFilename`, `os.Root`-pinned exclusive creation, 25 MiB cap) since filenames come from an untrusted remote source.
- Keeping the Gmail client fully decoupled from Bubble Tea required deliberate interface boundaries (`interfaces.go`) so async commands could be tested without a live terminal or real network.

## Future Improvements

- Multi-account support and account switching.
- Offline/local message index for faster search.
- Configurable keybindings instead of a fixed scheme.
- Thread-level (conversation) view rather than flat message list.

## Screenshots

| Inbox | Compose |
| --- | --- |
| ![Inbox](images/inbox.png) | ![Compose](images/compose.png) |

| Attach (compose) | Attach (received) |
| --- | --- |
| ![Attach send](images/attach_send.png) | ![Attach receive](images/attach_rec.png) |


## OAuth, storage, and privacy

For least-privilege browsing, start with:

```bash
GMAIL_TUI_READ_ONLY=1 go run .
```

This mode requests only `https://www.googleapis.com/auth/gmail.readonly` and disables compose, reply, send, trash, archive, and read/unread changes in both the UI and API client. Searching, viewing labels/messages, and attachment downloads remain available. The default mode retains the existing readonly/send/modify scope requests; startup does not add scopes to an existing token. Google documents that [gmail.modify also permits sending](https://developers.google.com/workspace/gmail/api/auth/scopes). Deselecting `gmail.send` while granting modify therefore still permits sending. A compose grant also permits sending. Scope checks use complete scope names, not substrings. Old tokens without stored scope information allow browsing but disable writes until reauthentication. When an OAuth response omits scope, OAuth's requested-scope default is used for that new authorization.

**Changing requested scopes does not revoke an existing broad grant.** Read-only mode blocks writes locally even with an old token, but reducing Google's authorization requires revoking the app's access in your [Google Account connections](https://myaccount.google.com/connections), removing `~/.gmail-tui-token.json`, and authorizing again with read-only mode enabled. Account revocation and browser consent are manual operations; automated tests never access a live account or send mail.

The desktop-client loopback flow binds only `127.0.0.1` on an ephemeral port, checks random state, and binds the authorization code to an S256 PKCE verifier. The existing manual code fallback also uses PKCE. OAuth exchange and automatic refresh failures omit provider response bodies before errors reach logs or the UI, and malformed credential errors omit the credential contents.

`~/.gmail-tui-token.json` contains **plaintext access and refresh tokens**. It is not encrypted or stored in an OS keychain. Writes use a unique 0600 temporary file in the same directory, sync and close it, then atomically replace the token path. Existing destination symlinks are replaced rather than followed; predictable `.tmp` paths are unused. Loading rejects symlinks and group/world-readable tokens. Keep the home directory private; these measures do not protect against another process running as your user, a compromised OS, or an untrusted parent directory. Unreadable, corrupt, symlinked, or publicly readable cached tokens require repair/removal before authentication. Refresh failure also stops with instructions to reauthenticate, rather than automatically requesting the default broader scopes. Protect `credentials.json` too. Exclude credentials, tokens, logs, and downloaded mail from source control and shared backups; encrypt any necessary private backups. Removing a local token alone does not revoke Google's grant.

Expected network traffic is Google OAuth authorization/token refresh and Gmail API requests. Application logs are local JSON at `~/.config/gmail-tui/app.log`; mail is cached in memory and attachments are saved only on request. No third-party analytics exporter is configured in the reviewed application path. Indirect OpenTelemetry dependencies do not establish that telemetry is exported: the Gmail service receives the app's authenticated HTTP client. This is a source review of this path, not exhaustive certification of every dependency or distributed binary. Treat logs and downloads as private account data.

Untrusted messages, headers, snippets, labels, filenames, and API errors are sanitized at terminal display boundaries. CSI, OSC (including clipboard OSC52 and hyperlink OSC8), DCS/APC/PM/SOS and C1 controls, carriage returns, backspace, and directional override/isolate controls cannot execute terminal instructions. Unicode text and body line breaks/tabs remain readable, and app styling is applied after sanitization. Original messages and outgoing quoted text are preserved. This addresses terminal display/clipboard manipulation; no remote code execution was demonstrated.

Attachment downloads are capped at 25 MiB, with an encoded response limit before JSON decoding. Sanitized basenames are created exclusively with 0600 permissions in a pinned `os.Root` directory. Existing files and symlinks, including dangling links, force a new numbered name; exhaustion fails without overwriting. Directory symlinks deliberately selected by the user remain supported. This is local filesystem/race hardening: a remote sender alone cannot create a local symlink. A process with control of the selected directory can still remove or rename files after creation.

## Automated security checks

Use the Go version required by `go.mod` (or a newer supported security patch). Dependency updates in this hardening include Go 1.26.10, x/net v0.60.0, and gRPC v1.83.2 with their required transitive updates. The completed source `govulncheck` scan reported zero called or imported-package vulnerabilities. It still reports module-level GO-2026-5932 for the unmaintained `golang.org/x/crypto/openpgp` package; this app does not import that package, and the advisory has no fixed version. These results describe static call reachability, not proof of exploitability or exhaustive certification.

```bash
go test ./api ./ui -run 'TestSecurity|TestLoadOAuthConfig|TestSaveToken|TestLoadToken|TestPerformLoopbackFlow|TestDownloadAttachment' -count=1
go test -race ./...
go vet ./...
go build ./...
govulncheck ./...
```

The offline tests use temporary files, mock Gmail transports, and local HTTP servers. They cover terminal rendering/list delegates, PKCE/state, secret redaction, token permissions/replacement, effective grants/read-only policy, attachment symlinks/concurrent reservations/collision exhaustion, and incoming size limits. `loop.config.mjs` and the tracked `.loop/prompts` configure implementation, docs, gates, review/repair, and draft PR publication against the fork's `main`. `.loop` run state, `.loop-worktrees/`, token files, and `app.log` are git-ignored. Root-level `make coverage` reports (`coverage.out`, `coverage_api.out`, `coverage.html`), the Makefile's `.tmp/` directory, and default local attachment `downloads/` directory are also git-ignored. The build gate supplies Git context computed with `git rev-parse` so VCS stamping works in linked worktrees even when an ancestor has an invalid `.git` directory. The operator handles merge; this flow does not release or contact upstream.
