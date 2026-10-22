# Changelog

All notable changes to this project will be documented in this file.

The format is based on Keep a Changelog and this project aims to follow Semantic Versioning.

## [Unreleased]

### Added

- OAuth loopback flow: first launch opens the browser and captures the redirect on a local port automatically; manual code entry remains as a headless fallback.
- Archive (`a`) removes the selected or open email from the inbox.
- Refresh (`R`) reloads the inbox from the first page.
- List title reflects context: `Inbox`, `Search: <query>`, or the label name, with an unread count for the inbox.
- Configurable inbox query via `inbox_query` / `GMAIL_TUI_INBOX_QUERY` (default `in:inbox category:primary`).
- Inbox rows now show the email date alongside sender and snippet.

### Changed

- HTML bodies now keep line structure (`<br>`, `</p>`, headings become newlines) and drop `<script>`/`<style>` content entirely.
- Outgoing subjects are RFC 2047 encoded, all user-supplied headers are CRLF-sanitized, and attachment filenames use RFC 2231-safe `Content-Disposition` formatting.
- Snippets are HTML-entity decoded and truncated on rune boundaries.
- OAuth state parameter is now cryptographically random and verified on callback (was a static string); the gitignore warning only fires when using a local `credentials.json`.
- Status messages now always auto-clear; `q`/`ctrl+c` quits from the loading screen.
- Capability checks follow the granted scopes. A `gmail.modify` or `gmail.compose` grant now enables sending even if `gmail.send` was deselected, because Google's `gmail.modify` scope also permits sending.
- Cached tokens are rejected when they are symlinks or group/world-readable. Fix with `chmod 600 ~/.gmail-tui-token.json`, or remove the file and reauthenticate.
- Attachment name collisions get numbered names (`name(1).ext`) and fail after 9999 candidates instead of overwriting an existing file.
- Go toolchain requirement raised to 1.26.10; `golang.org/x/net` updated to v0.60.0 and `google.golang.org/grpc` to v1.83.2.

### Fixed

- Pagination: the initial inbox fetch discarded the next-page token, so `n` never advanced; `p` also refetched the current page instead of the previous one. Both work now, and search/label results reset stale inbox pagination state.
- Reply `References` header now appends the replied message's `Message-ID` per RFC 5322, fixing thread grouping in strict clients.
- Toggling read/unread while viewing an email updates the open email, not just the list row.
- Cache reads return copies and unread updates happen under the cache lock, removing a data race with concurrent fetch workers.

### Security

- Untrusted text (inbox rows, labels, snippets, headers, message bodies, attachment names, status and error messages) is stripped of terminal control sequences before display. This covers CSI, OSC (including OSC 52 clipboard and OSC 8 links), DCS/APC/PM/SOS, C1 controls, carriage returns, backspace, and bidi override/isolate controls. Sent message content is not modified.
- OAuth loopback and manual flows use PKCE (S256). Random state and 127.0.0.1-only loopback binding are unchanged.
- Token writes use a unique 0600 temporary file and an atomic rename. Symlinked token paths are rejected on load and replaced on save.
- OAuth exchange and refresh errors, and malformed credential errors, no longer include provider response bodies or credential contents.
- Added `GMAIL_TUI_READ_ONLY=1`, which requests only `gmail.readonly` and disables send, compose, reply, trash, archive, and read/unread changes. Existing broad tokens are not revoked by this mode; see the README privacy section.
- Attachment downloads are created with exclusive no-follow semantics inside a pinned directory, and incoming attachment responses are capped at 25 MiB. This is local filesystem and race hardening, not a remote-overwrite fix.


- Configuration loading from TOML and `GMAIL_TUI_*` environment variables.
- Structured JSON logging and a bounded LRU email cache.
- Pagination, improved search UX, compose/reply validation, and reply threading support.
- Unit, property, and integration test coverage for the API and UI packages.
- CI workflow, pre-commit hooks, linting configuration, and project documentation.

### Changed

- Replaced broad Gmail scope usage with readonly and send scopes.
- Hardened attachment downloads, token persistence, and parser edge-case handling.
- Improved compose and reply flows with validation and no-subject confirmation.

### Fixed

- Consistent error-prefix handling in the status bar.
- Safer HTML stripping, MIME recursion handling, and body decode fallbacks.
- Correct reply subject normalization and Gmail threading headers.
