# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

CodeScout is a self-hosted logging and network inspection tool for Flutter apps. This repository is the dashboard: it receives batched uploads from the SDK (gzipped tar), stores them, and serves a web UI built with Templ + HTMX + Tailwind CSS. It also streams live sessions from a paired device.

The Flutter SDK lives in its own repository, [code_scout_flutter](https://github.com/getcodescout/code_scout_flutter).

## Tech Stack

- **Go 1.25** with Gorilla Mux (routing), GORM (ORM), Logrus (logging)
- **Postgres 16** — boot config via TOML at `/etc/code-scout.conf`; everything else is a database-backed instance setting edited in the UI
- **Templ** — HTML templating (`.templ` files generate `_templ.go` files)
- **Tailwind CSS 3.4** + **HTMX 2.0** for the frontend
- **gorilla/websocket** for the live device socket; watchers use SSE
- **air** for hot-reload during development

## Build & Development Commands

```bash
make dev-setup    # Write /etc/code-scout.conf, create the Postgres role and database
make run          # Start dev server (templ watch + air hot-reload) on :24275
make build        # Build Linux/AMD64 binary to ./bin/code_scout
make test-all     # Full Go suite including integration tests, against a throwaway database
make test-e2e     # Playwright against a real server on :24283
make test-e2e-headed  # The same, in a window you can watch
make test-sdk-e2e # The real Flutter SDK against a real server on :24284
make tailwind     # Watch and rebuild Tailwind CSS
```

`make templ` is a **watch** command and will not return. To regenerate once:
```bash
TEMPL_EXPERIMENT=rawgo templ generate
```

Run a single test:
```bash
go test ./path/to/package -run TestName
```

Default dev server port: **24275**, templ proxy port: **9089**.

## Architecture (Hexagonal)

```
main.go                      → Entry point: wires dependencies, starts server
app/                         → App metadata (version constant)
conf/                        → TOML config loader (/etc/code-scout.conf)

internal/domain/             → Domain entities (Project, Log, User) & error codes/messages
internal/ports/              → Interfaces: repositories + services (contracts)
internal/services/           → Business logic (ProjectService, LogService, AuthService)
internal/adapters/db/        → GORM persistence: connection, models, mappers, repos

server/                      → HTTP layer
  server.go                  → Server struct, Run(), graceful shutdown
  routes.go                  → Route registration + middleware chain
  handlers/                  → HTTP handlers (project, log, view, auth, response helpers)
  middleware/                → Auth, CORS, logging, recovery, pagination, session

pkg/cslog/                   → Custom logging framework (Logger, RequestLog, hooks)
pkg/utils/                   → Shared utilities (GormBase, errors, HTTP helpers, pagination)

view/                        → Templ templates (.templ) + static assets (CSS, JS, images)
jobs/                        → Cron scheduler (robfig/cron)
```

**Request flow:** HTTP request → middleware chain (logger → close-conn → CORS → JSON content type → auth) → `server/handlers/` → `internal/services/` → `internal/adapters/db/` → Postgres

`internal/live/` is the exception: live sessions are in-memory only and never touch the database.

**The live socket is request/reply as well as fan-out.** `Hub.Publish` sends one device's events to
many watchers; `Hub.Ask` sends one dashboard's question to the device and matches the answer back by
request id. That second direction is what the database browser rides on. Three orderings are
load-bearing and each has a test: the pending entry is registered *before* the frame is written, the
socket write happens *outside* the hub lock, and `endLocked` releases every waiter rather than
leaving them to time out.

**Every write to a device socket goes through `deviceConn`.** gorilla/websocket panics with
`concurrent write to websocket connection` on a second concurrent writer — it does not silently
interleave, whatever you may have read. The mutex is what keeps the ping ticker and a dashboard
query from colliding.

## Key Patterns

- **Hexagonal architecture**: Domain entities in `internal/domain/`, interfaces in `internal/ports/`, implementations in `internal/services/` and `internal/adapters/db/`.
- **Dependency injection**: No global DB variable. `*gorm.DB` is passed explicitly from `main.go` through constructors. Repositories and services accept interfaces.
- **Domain/GORM separation**: Domain entities have no ORM tags. GORM models in `internal/adapters/db/models.go` with mappers for conversion.
- **GormBase**: All DB models embed `pkg/utils.GormBase` which provides UUID primary keys (not auto-increment) and soft deletes.
- **API authentication**: Protected routes require `X-Project-ID` and `X-Project-Secret` headers validated against the `project_secrets` table.
- **Templ workflow**: Edit `.templ` files, never edit generated `_templ.go` files directly. Running `make run` handles regeneration automatically. Templ uses `TEMPL_EXPERIMENT=rawgo` flag.
- **Request-scoped logging**: `cslog.NewRequestCtrl(r)` creates a `RequestLog` from an HTTP request with `request_id` and `user_id` fields. Services/handlers embed `cslog.BaseCtrl` via `RequestCtrl` interface to carry logging context. Use `cslog.L(ctx)` to retrieve the logger from context.
- **Build metadata**: Binary embeds build time, git branch, commit hash, and dirty file status via ldflags.

## API Routes

- **Public**: `GET /login`, `GET /healthz`, `GET /static/*`
- **Auth API** (no session needed): `POST /api/auth/submit` (login or register), `POST /api/auth/logout`
- **Protected web pages** (require `cs_session` cookie): `GET /`, `/settings`, `/account`, `/members`, and everything under `/project/{id}`
- **SDK routes** (`/api/*`, require `X-Project-ID` + `X-Project-Secret` headers): `POST /api/logs/dump`, `GET /api/validate`, `GET /api/live/socket` (WebSocket upgrade)
- **MCP** (`/api/mcp`, requires `Authorization: Bearer csp_…`): registered before the SDK subrouter so it matches first. See The MCP endpoint below.

**The database browser's only cross-repo test lives in the SDK repo.** Playwright answers the
device socket with a stub, so it proves this server renders what a device says and nothing about
what a device actually says. `test/e2e/db_browser_test.dart` over there pairs the real SDK and
drives these routes against a real SQLite file, and `make test-sdk-e2e` runs it. Change a field
name in `internal/domain/live_db.go` and that is what goes red.

The live session's **database browser** hangs off `/project/{id}/live/{sid}/db`. Reading (`/db`,
`/db/rows`, `/db/cell`) needs project read, the same bar as watching a stream. Writing
(`POST /db/cell`) sits on the **manage** subrouter: it reaches into somebody's phone and changes
what is stored there, which belongs with rotating a secret rather than with reading logs. The app
must also have registered that database `writable`, so it is the second of two gates.

**One list template serves the Network screen and a session's Network tab.** They are the same
`NetworkData` and the same `/network/inspector` fragment; the only difference is a `SessionID` on
the filter, which swaps the waterfall column for a clock and a distance from launch, and points a
selection at `/session/{sid}` instead of `/network`. The rows the fragment swaps back out of band
have to come from the same template as the ones on the page — rebuilding them in the other shape
puts every cell under the wrong heading. The inspector's tab travels as `phase=`, never `tab=`,
because the session screen already spends `tab` on Logs and Network.

**Behind a reverse proxy**, the live features need `Upgrade`/`Connection` forwarded and
`proxy_buffering off`. A default nginx config breaks both with nothing in any log to say so — see
the README's Configuration section.

Everything project-scoped hangs off a `/project/{id}` subrouter behind `RequireProjectAccess`, so a handler cannot forget the check. A project the caller cannot see answers **404, never 403**.

## Database

Eleven tables, auto-migrated on startup: `projects`, `project_secrets`, `project_members`, `project_favorites`, `users`, `user_sessions`, `personal_access_tokens`, `instance_settings`, `project_usage_daily`, `sessions`, `logs`.

- `users` — `email` (unique, lower-cased before storing), `name`, `password_hash` (bcrypt), `role`, `must_change_password`. Email is the login identifier; there is no `username`.
- `user_sessions` — `user_id`, `token`, `expires_at` (30 days)
- `personal_access_tokens` — `user_id`, `token_hash` (hex SHA-256, unique), `suffix`, `last_used_at`, `expires_at`. The MCP endpoint's credential. Unlike `user_sessions.token` the plaintext is never stored, because a token lives in editor configs for months rather than in one browser.
- `sessions` — one row per app launch, keyed on the client's own session id
- `instance_settings` — a single row: timezone, retention, upload cap, daily log cap. Read live, so a change applies without a restart.

Live sessions are deliberately **not** in this list. They exist only in memory in `internal/live/`.

## The MCP endpoint

`/api/mcp` serves MCP over streamable HTTP (`server/mcptools/`), so a coding agent can read
logs, error groups, sessions, network calls, live sessions and a paired device's databases —
everything read-only. It lives **in this process on purpose**: `internal/live.Hub` is in-memory,
so live tools are only possible here, and being a URL on the server the dashboard already runs
means nothing to install client-side.

**Auth is a personal access token**, `Authorization: Bearer csp_…`, minted per user on `/account`
(Personal settings → API tokens — personal, so deliberately not on the instance settings screen).
Only a SHA-256 hash is stored (`personal_access_tokens`), the plaintext is shown once,
and `TestThePersonalTokenIsNeverLogged` holds for it — twice, once at the repo and once over the
full HTTP stack. A token grants exactly what its user can see: tools resolve access through the
same `MemberService.ResolveAccess` the web router uses, and a project the user cannot read answers
"project not found", byte-identical to one that does not exist.

**Read-only is structural, not policed.** Each database-browser tool hardcodes its op literal
(`sources`/`namespaces`/`schema`/`rows`); no input carries an op, args, SQL or a value; the rows
limit is a server constant under the device's page budget. `TestNoToolCanExpressAWrite` walks the
registered tools and fails on a write-shaped name or input field. Do not add a generic
"ask the device" tool — that ends the argument, same as the dashboard rule.

**Errors are sanitised at the source.** The SDK packs a returned Go error's text into the tool
result (`sdk_shape_test.go` pins this), so unexpected errors go through `internal()`, which logs
the real thing and returns a bare "internal error". Never return a raw service error from a tool.

**Every tool registers through `addTool` in `server.go`, never the SDK's `mcp.AddTool`.**
`mcp.AddTool` checks typed output by decoding it into `any` and then sends a marshal of that copy,
so every number goes through a float64: a body's `12.0` reaches the agent as `12`, and
`9007199254740993` as `9007199254740992`. `TestTheSDKSendsEveryNumberThroughAFloat64` pins the SDK
doing it. `addTool` sends the bytes `json.Marshal` wrote for the handler's output, after checking
them against the inferred output schema itself. That check is why free-form output fields are
`any`: `json.RawMessage` infers as an array of bytes, so one carrying an object fails it and the
call answers "internal error". A tool registered with `mcp.AddTool` still lists, answers and passes
every test that does not look at a number, so `TestOnlyAddToolCallsTheSDKsAddTool` parses this
package and fails on any `mcp.AddTool` outside `addTool`.

**Stored JSON is decoded with `UseNumber`, never with a plain `json.Unmarshal` into `any`.**
Metadata, bodies, tags and stack traces are stored as the bytes the app sent. A plain decode makes
every number a float64, which prints `12.0` as `12` and rounds an integer above 2^53, and that
difference is often the whole story of a decode failure: the app's model wanted a double and got an
int. The dashboard decodes through `decodeJSON` in `view/network_helpers.go`, behind `decodeMeta`
(the inspector's bodies and headers) and `prettyJSON` (metadata in the log viewer and on the call
page). MCP decodes through `rawJSON` in `format.go`. A number too large for any float64 makes
`rawJSON` omit the value holding it, because the schema check and any client reading a float64
would refuse the whole result. `TestNumbersRenderAsTheAppSentThem` and
`TestNumbersReachTheAgentAsTheAppSentThem` fail on a plain decode.

**A call's phases and the app's own logs about it are told apart on `is_network_call` alone.** An
app log can carry a call's `request_id` with `is_network_call` false and no `call_phase`, so a
shared request id does not make a log a phase. `domain.SplitCallLogs` splits what `GetByRequestID`
returns on `IsNetworkCall`, and `get_network_request` (its `phases` and `linked_logs`), the
inspector and the call page all use it. Every other pairing site reads the same column and never
`request_id`: `ListNetworkCalls` and `GetByRequestID` in SQL, `phaseLog`, the session and overview
counts, and the live view's network gate. Pair on `request_id` and an app log turns into a pending
call of its own, stretches its call's duration, or reads to an agent as the call's error phase.

The transport is `Stateless: true, JSONResponse: true`: every POST is one JSON body, no session
state, nothing held open against the 30s WriteTimeout. **`DisableLocalhostProtection: true` is
load-bearing, not a hardening flag to restore**: the SDK's DNS-rebinding guard refuses a loopback
connection carrying a public Host header, which is exactly what nginx forwards, and the first dev
deploy died on `Forbidden: invalid Host header`. The bearer middleware in front is what makes
disabling it sound — a rebinding attack cannot carry the Authorization header.
`TestAProxiedHostHeaderIsNotRefused` fails if it comes back. A reverse proxy needs nothing special
for `/api/mcp` — plain POSTs, unlike the live features' WebSocket and SSE. Client setup:

```bash
claude mcp add --transport http code-scout http://localhost:24275/api/mcp \
  --header "Authorization: Bearer csp_…"
```

`TOKEN=csp_… make mcp-smoke` pokes the endpoint by hand. The public guide is
`landing_and_docs/.../guides/mcp.md`.

## Versioning and releases

**`app/version.go` holds the version, and it is the only place.** A constant in the source rather
than something the linker injects, so a local build, a `go install` and a release image all report
the same honest answer instead of "dev". `Commit` and `BuildTime` are ldflag vars in the same
package, set by three build paths that have to stay in step: the Makefile's `build` and `deploy`
targets and the Dockerfile. They had already drifted once, with a build arg named `VERSION` going
into a variable called `BranchName`.

**`CHANGELOG.md` is the release notes.** Keep a Changelog format, `## [1.2.3] - date` headings
exactly, because CI extracts the matching section by pattern and fails the release when it finds
nothing. Write the entry as part of the change.

**To release:** bump `app.Version`, date its changelog section, commit, then the owner tags
`vX.Y.Z`. CI refuses a tag that disagrees with the constant, before it publishes any image.
**Never cut a tag unasked.**

**The image goes to `ghcr.io/getcodescout/code_scout`**, not Docker Hub. Docker Hub charges for an
organisation, and publishing a project's only distribution channel from one person's personal
account ties it to that individual's account, handle and 2FA. GHCR is free for public images,
inherits access from the org, and needs no stored secret: the `packages: write` permission on the
publish job plus the run's own `GITHUB_TOKEN` is the whole of it. `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` are gone and should not come back.

The `org.opencontainers.image.source` label in the Dockerfile is what links the package to this
repository. Without it the image is an orphan in the org's package list. **A newly published GHCR
package is private until somebody makes it public**, which is a one-time click in the org's package
settings and is the reason a first pull returns `denied`.

**The update check** (`internal/services/version_service.go`) asks GitHub once a day and holds the
answer in memory only. It is a cache, not a setting. It skips entirely when the setting is off
*and* when `InstanceSettingsService.Loaded()` is false: settings fail open, and not knowing whether
someone disabled an outbound request is not permission to make it.

## Logging

**Never log a credential.** The session token was written at debug on every request carrying a
cookie, and debug was the hardcoded level, so a running instance accumulated live `cs_session`
values anyone with journal access could use. `TestTheSessionTokenIsNeverLogged` asserts on the
logger's real output rather than on call sites, because a call-site assertion passes until the next
`WithField`. The same rule covers stack traces: a panic gets a plain 500 and the stack goes to the
log, never into the response.

**`cslog.Configure` is called once, straight after `confs.Load()`**, and reconfigures the logger
that package init already built. Anything logged before that goes to stderr, which is where a
configuration failure belongs. `log_file` empty means stdout; set it and lumberjack rotates it.
Errors and fatals mirror to stderr even then, so a fatal at boot is visible in `journalctl` and not
only in a file.

**`HttpLogger` and `Recovery` are both on the root router, logger outermost.** It used to be on two
subrouters, so no dashboard page was logged at all and the database lines those requests produced
had no `request_id`. The order is load-bearing: Recovery turns a panic into a 500 and returns
normally, so the request still gets its line with status 500 on it. Reversed, the panic unwinds
past the logging and the request disappears from the log entirely.

`/healthz` and `/static/` log at debug via `routine()` — a probe every 15 seconds is most of the
volume and none of the information.

## Design System
Always read `DESIGN.md` before making any visual or UI decisions.
All font choices, colors, spacing, and aesthetic direction are defined there.
Do not deviate without explicit user approval.
In QA mode, flag any code that doesn't match DESIGN.md.
