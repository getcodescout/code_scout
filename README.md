<p align="center">
  <img src=".github/assets/pim_code_scout.svg" alt="CodeScout" width="320" />
</p>

<p align="center">
  Self-hosted logging and network inspection for Flutter apps.
</p>

<p align="center">
  <a href="https://github.com/getcodescout/code_scout/actions/workflows/ci.yml"><img src="https://github.com/getcodescout/code_scout/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/getcodescout/code_scout/pkgs/container/code_scout"><img src="https://img.shields.io/badge/ghcr.io-code__scout-2496ED?logo=docker&logoColor=white" alt="Container image"></a>
  <a href="https://pub.dev/packages/code_scout"><img src="https://img.shields.io/pub/v/code_scout.svg?label=flutter%20sdk" alt="Flutter SDK on pub.dev"></a>
  <img src="https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white" alt="Go 1.25">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT"></a>
</p>

<p align="center">
  <a href="https://codescout.tech">Website</a> &middot;
  <a href="https://codescout.tech/docs/">Documentation</a> &middot;
  <a href="https://github.com/getcodescout/code_scout_flutter">Flutter SDK</a> &middot;
  <a href="https://github.com/getcodescout/code_scout/pkgs/container/code_scout">Container image</a>
</p>

---

Your Flutter app is transparent while it is attached to your machine, and a black box the moment
it is not. A build on a QA phone, a UAT device or a tester's handset gives you nothing back except
what somebody thinks to tell you, and every question after that costs a round trip.

CodeScout closes that gap. It is not a replacement for DevTools, which is better than this at
everything you can do while your device is plugged in. This is for the build that has already left
your machine.

Add one package to your Flutter app. Every log line and every HTTP request gets captured, printed
to your console, saved on the device, and sent to a dashboard you run yourself. From there you can
search it, filter by tag, replay a session, or watch a phone live while someone reproduces a bug in
front of you.

CodeScout is not a crash reporter. Crashlytics tells you the app crashed. CodeScout shows you
what it was doing for the five minutes before. Plenty of teams run both.

**You host it.** Your logs go to your server and your database. There is no account to sign up for
and no usage tier, because there is nobody in the middle.

<p align="center">
  <img src=".github/assets/screenshots/logs.png" alt="The log viewer" width="900" />
</p>

## Try it

You need Docker. Nothing else.

```bash
curl -O https://raw.githubusercontent.com/getcodescout/code_scout/main/docker-compose.yml
docker compose up
```

One file, and it pulls the released image rather than building anything. That starts CodeScout and
a Postgres database, and creates the tables on first run. Open <http://localhost:24275>.

The first page asks you to register. The first account you make becomes the super admin, which is
the role that sees every project and can change instance-wide settings. After that the same page
becomes an ordinary login, and further accounts are made from the Members screen rather than by
registering again.

Create a project, and its ID and secret appear on the last step. You can read the secret again
later under Settings and then SDK setup, or rotate it if it leaks. That tab only appears for
people with manage rights on the project, so a read-only member will not find it.

> Please change the passwords in `docker-compose.yml` before putting this anywhere other people can
> reach.

### Pointing it at a database you already have

If you already run Postgres somewhere, whether that is RDS, Cloud SQL or your own box, delete the
`db` service from `docker-compose.yml` and give the app your connection details instead.

```bash
docker run -p 24275:24275 \
  -e CS_DB_HOST=your-db.example.com \
  -e CS_DB_USER=code_scout \
  -e CS_DB_PASSWORD=secret \
  -e CS_DB_NAME=code_scout \
  -e CS_DB_SSLMODE=require \
  ghcr.io/getcodescout/code_scout:latest
```

## Connecting your app

```bash
flutter pub add code_scout
flutter pub add code_scout_dio    # if you use Dio
flutter pub add code_scout_http   # if you use package:http
```

Start it once, early in `main`:

```dart
await CodeScout.instance.init(
  freshContextFetcher: () => context,
  configuration: CodeScoutConfiguration(
    logging: LoggingBehavior(minimumLevel: LogLevel.all),
    projectCredentials: ProjectCredentials(
      link: 'http://localhost:24275/',
      projectID: 'your-project-id',
      projectSecret: 'your-secret-key',
    ),
    sync: LogSyncBehavior(syncInterval: Duration(seconds: 30)),
  ),
);
```

Then log things:

```dart
final scout = CodeScout.instance;

scout.d('Cart restored from cache');
scout.i('Checkout started', tags: {'analytics', 'checkout'});
scout.e('Payment failed', error: e, stackTrace: st);
```

And capture your network calls by wrapping the client you already have:

```dart
dio.interceptors.add(CodeScoutDioInterceptor());              // Dio

final client = CodeScoutHttpClient(client: myExistingClient); // package:http
```

Pass your existing client in. If you leave `client:` out, the wrapper builds a plain new one and
any base headers, proxy or timeout you had configured are quietly lost.

`projectCredentials` is optional. Leave it out and CodeScout is a local logging library: you get
console output and an on-device viewer, and nothing leaves the phone. Add the credentials when you
want the dashboard as well.

Full setup guide: [codescout.tech/docs](https://codescout.tech/docs/).

## What you get

### Logs

The screen at the top of this page. Levels you can switch off one at a time, your own tags as chips
with counts, a time window, and infinite scroll.

Every control is a link, so the address bar always describes what you are looking at. There is no
client-side state anywhere in the screen, which is why pasting that URL to a colleague shows them
exactly what you were looking at.

The search box takes a small query language, and you can mix it with plain text.

| | |
|---|---|
| `level:error` | only that level. Repeat it for more: `level:error level:fatal` |
| `tag:checkout` | logs carrying a tag |
| `-tag:heartbeat` | everything except that tag |
| `session:4f2a81b0-9d3c-4e77-b0a1-2f9c6d5e8a41` | one app launch |
| `request:7d19c204-1b6e-4a52-9c88-3ee1f0a7b942` | one network call: its request, its response or error, and any log your app linked to it |
| `user:ada@example.com` | everything that happened to one person |
| `installation:9eec2f07-52c1-4a90-8e6b-77d0c3b41f28` | one install, across launches |
| `app_version:3.11.2` | one build of your app |
| `device:Pixel` | a device model, matched loosely |
| `os:Android` | an OS name or version |
| `"gateway timeout"` | plain text in the message |

`level:` is a set rather than a threshold, so `level:error` on its own does not include fatals.
That is worth remembering during an incident, which is exactly when you would type it.

The three id filters take the whole UUID. A shortened one is refused outright rather than
matched as a prefix, so paste the id rather than the first few characters of it. `user:`,
`installation:` and `app_version:` all match exactly, while `device:` and `os:` are the two that
match loosely.

### Network

The SDK records a call's request, and then its response or its error, as separate logs. The
dashboard pairs them back into one row per call, with a waterfall showing when each one ran and how
long it took. A call whose answer was never recorded shows as pending.

<p align="center">
  <img src=".github/assets/screenshots/network.png" alt="Network inspector" width="880" />
</p>

Headers, payload and response body each get their own tab, the same way browser dev tools do.
Anything the SDK redacted shows as a redaction rather than as the value.

When your app logs something about a call with its request id, such as the error its model threw on
a response body, the inspector lists that log above the tabs under **Logged by the app**, so the
error and the body that caused it are read together. The call itself is unchanged: a 200 the app
could not read is still a 200.

### Errors

The same bug usually arrives thousands of times with slightly different wording. Errors are grouped
by shape, so `User 4821 not found` and `User 9134 not found` are one row and one problem. Open a
row for the latest stack trace, then jump to every occurrence or to the launch it last happened in.

<p align="center">
  <img src=".github/assets/screenshots/errors-expanded.png" alt="Errors grouped by shape, with one row expanded to its stack trace" width="880" />
</p>

Network failures are grouped differently, on the method and path, because every one of them carries
the same message. Otherwise a timing-out payment gateway and a blocked analytics ping would share a
row.

### Sessions

A session is one run of your app. This is all of it, in order, with the time since launch on every
row, which is what lets you see that the token refresh at +5:06 came back 401 and the payment went
out at +7:00 with the old one.

<p align="center">
  <img src=".github/assets/screenshots/session-detail.png" alt="A session timeline: every log from one app launch with the time since launch beside it" width="880" />
</p>

### Devices

Every install that has ever reported, rolled up by a stable installation id rather than by user, so
one phone stays one row across sign-ins. Sessions, errors and last seen, with the app version
beside them. This is where "is it only that build?" gets answered.

<p align="center">
  <img src=".github/assets/screenshots/devices.png" alt="Devices: every install that has reported, with sessions, errors and last seen" width="880" />
</p>

### Live devices

Most of this is about reading the past. This part is not.

Pair a phone with a short code from the dashboard, and watch its logs and network calls arrive
while the bug is reproduced in front of you. A tester can pair their own phone and watch it
themselves. It is the difference
between being sent a description of a problem and watching it happen.

Nothing streamed this way is stored, so it is safe to point at a build you would not want filling
up your database.

<p align="center">
  <img src=".github/assets/screenshots/live.png" alt="A paired device streaming live" width="880" />
</p>

### The device's own database

While a device is paired you can open the storage the app keeps on the phone: SQLite tables,
`shared_preferences`, Hive boxes. Page through the rows, and change one value at a time.

Half of "cannot reproduce" is stale data on somebody's device, and this is how you find out. Flip a
feature flag and watch the app react, or clear a cached token that has got into a state the code
cannot recover from.

Nothing is browsable until your app names it with `registerDatabase`, nothing is editable unless
you pass `writable: true`, and none of it is copied to your server. That is the opposite of
redaction, which hides nothing until you say so, and the inversion is deliberate: a log is what
your app chose to write, a database is everything it has.

<p align="center">
  <img src=".github/assets/screenshots/database.png" alt="Browsing a paired device's SQLite tables" width="880" />
</p>

### Your coding agent can read all of it

CodeScout speaks [MCP](https://modelcontextprotocol.io), so the agent you already have open can
search your logs, read grouped errors, walk a session from start to finish, inspect network calls,
and read a paired device's local databases. Create a token under Personal settings and point a
client at it. It is a URL and a bearer header, so any MCP client will do:

```json
{
  "mcpServers": {
    "code-scout": {
      "url": "https://logs.example.com/api/mcp",
      "headers": { "Authorization": "Bearer csp_your_token" }
    }
  }
}
```

Claude Code, Cursor, VS Code and Windsurf each want that spelled a little differently, and the
[MCP guide](https://codescout.tech/docs/guides/mcp/) has the exact block for each.

Then ask it something. The handover this is really for: a tester hits a bug, copies the report out
of the app's overlay, and sends it over. That report carries the session id, so the developer
pastes it into their agent and the agent reads the whole launch back instead of working from a
description of it.

Every tool is read only, and that is a property of what they can express rather than a rule
enforced somewhere: none of them takes an operation, a statement, or a value to write. A token sees
exactly the projects its owner sees. See
[Reading CodeScout with an AI agent](https://codescout.tech/docs/guides/mcp/).

### Overview

The first screen of a project: counts for the window you pick, an activity chart, and the errors
that happened most recently. The range lives in the address bar, so last 24 hours, 7 days and 30
days are three links rather than three clicks and a lost place.

<p align="center">
  <img src=".github/assets/screenshots/overview.png" alt="Project overview: stat tiles, a stacked activity chart and recent errors" width="880" />
</p>

### Accounts and access

Three roles for the instance plus a level per project. You see the projects you belong to and
nothing else. A project you cannot see answers 404 rather than 403, so the list of projects you
are not in stays private.

### Keeping the volume down

Session sampling per project, a daily cap per project, a cap on upload size, and retention. All of
it is read from the database as it is used, so changing a setting takes effect without a restart.

## About your credentials

Nothing is hidden unless you say so, because the auth header is quite often the reason a request is
failing, and a debugging tool that hides it is not much of a debugging tool.

`RedactionBehavior.recommended()` turns on the usual suspects in one line. Whatever you name is
stripped on the device, before anything is written to disk or uploaded. Decide this deliberately
before you point it at production, and read
[Redaction and privacy](https://codescout.tech/docs/guides/redaction/) first.

## How it fits together

```
Flutter app                              Your server
┌────────────────────────┐              ┌──────────────────────────┐
│ CodeScout.instance.i() │              │                          │
│ Dio / http interceptor │              │  POST /api/logs/dump     │
│          ↓             │  ──batched── │          ↓               │
│ SQLite (on device)     │   tar.gz     │  Postgres                │
│          ↓             │   upload     │          ↓               │
│ Sync worker            │              │  Dashboard + live tail   │
└────────────────────────┘              └──────────────────────────┘
```

Logs are written to SQLite on the device first, so nothing is lost when the network drops. A
background worker batches them up, compresses them off the main thread, and uploads. If an upload
fails the batch is put back and tried again later.

| | |
|---|---|
| **Dashboard** (this repo) | Go 1.25, Postgres 16, Templ, HTMX, Tailwind |
| **Flutter SDK** | [`code_scout`](https://pub.dev/packages/code_scout), [`code_scout_dio`](https://pub.dev/packages/code_scout_dio), [`code_scout_http`](https://pub.dev/packages/code_scout_http) |
| **SDK source** | [getcodescout/code_scout_flutter](https://github.com/getcodescout/code_scout_flutter) |

## Configuration

Everything is set with environment variables. You can put the same keys in `/etc/code-scout.conf`
as TOML if you prefer a file. The key is the variable name with `CS_` removed and the rest
lower-cased, so `CS_DB_HOST` becomes `db_host` and `CS_CONN_MAX_LIFETIME_MINUTES` becomes
`conn_max_lifetime_minutes`. Numbers go in unquoted: a quoted one is ignored and you silently
get the default instead. Environment variables win.

These are the ones you need to start:

| Variable | Default | |
|---|---|---|
| `CS_DB_HOST` | | required |
| `CS_DB_NAME` | | required |
| `CS_DB_USER` | | required |
| `CS_DB_PASSWORD` | | |
| `CS_DB_SSLMODE` | `disable` | `require`, `verify-ca` or `verify-full`. Managed databases usually want at least `require` |
| `CS_PORT` | `24275` | |
| `CS_PUBLIC_BASE_URL` | | the address people actually reach this instance on, if it sits behind a proxy |

The other twelve are the database port, the bind address, the connection pool and seven logging
keys, all with defaults that are fine until they are not. Every one of them, with its TOML name
and its default, is in [the configuration
reference](https://codescout.tech/docs/guides/server-setup/#configuration).

The server waits for the database on startup and retries, so it is fine to start both at once.
`GET /healthz` answers 200 when it is ready and 503 when the database is not, which is what the
container health check uses.

If you are locked out of the only owner account, `code_scout reset-password --email=you@example.com`
prints a temporary password once and signs that account out everywhere. Inside Docker that is
`docker exec <container> code_scout reset-password --email=you@example.com`. The binary is on
the PATH inside the image, so there is no `./` in front of it. Run it in the container that
already has the `CS_DB_*` settings, because the command reads the same configuration the server
does. The temporary password is printed once and stored nowhere, so copy it before you close the
terminal.

### Behind a reverse proxy

Two things here are not ordinary HTTP: live sessions upgrade to a WebSocket, and the dashboard
watches them over Server-Sent Events. A default nginx config forwards neither, **with nothing in
any log to say so**. Everything else keeps working, so it rarely looks like a proxy problem.

```nginx
map $http_upgrade $connection_upgrade { default upgrade; '' close; }

location / {
    proxy_pass http://127.0.0.1:24275;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_buffering off;
    proxy_read_timeout 3600s;
}
```

Caddy needs none of this: `reverse_proxy 127.0.0.1:24275` handles both. Full notes, including why
the `map` beats hardcoding the header, are in
[the setup guide](https://codescout.tech/docs/guides/server-setup/#behind-a-reverse-proxy).

## Working on it

```bash
make dev-setup   # first time: writes the config and creates the local database
make dev         # hot reloading dev server on :24275
make test-all    # unit and integration tests, against a scratch database
```

That is enough to get a server up. [CONTRIBUTING.md](CONTRIBUTING.md) has the rest: the other make
targets, what needs a real Postgres and why, how the schema gets rebuilt, the two generated files
that are committed, and how the packages fit together.

## Contributing

Issues and pull requests are welcome, and small ones are the easiest to accept. Please open an
issue before starting something large so we can check it fits where the project is going.

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to get set up and what a good pull request looks
like. Everything ships with tests, including the browser tests, and the honest way to check one is
to undo the fix and watch the test fail.

Right now the most useful contributions are anything that makes the first fifteen minutes easier
for somebody who has never seen this before, and browser test coverage for the screens the
current suite does not reach.

## Status

Version 1.0 is complete. The Flutter SDK is published on pub.dev and everything described here
works today.

The image is published to `ghcr.io/getcodescout/code_scout`. A release tag publishes `latest`
alongside its version tags (`1.1.0` and `1.1`); every push to `main` publishes `edge`. Use `latest`
unless you are chasing an unreleased fix. It is on the GitHub Container Registry rather than
Docker Hub, which needs a paid plan for an organisation, and publishing under one person's personal
account is not a thing to build a project's distribution on.

Deliberately left until after 1.0: alert rules, crash reporting, performance metrics, and full
text search.

## License

MIT. See [LICENSE](LICENSE).
