# Changelog

All notable changes to the CodeScout dashboard are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file is the source of the GitHub release notes. When a `v*` tag is pushed,
CI reads the section matching that version and publishes it, and the release
fails if there is no matching section. So writing the entry is part of making
the change, not something to remember at release time.

The version itself lives in `app/version.go`. CI checks the tag against it and
refuses to publish when the two disagree.

The Flutter SDK has its own changelog, in
[code_scout_flutter](https://github.com/getcodescout/code_scout_flutter).

## [1.2.0] - 2026-10-06

When a response body stops matching the app's model, the failure can now be
read beside the call that returned it.

### Added

- **Logs the app links to a call.** An app log that carries a call's request
  id, such as the error its `fromJson` threw on a body whose `tax_rate` changed
  from a double to an int, is now shown with that call. The Network inspector
  and the call page list it under **Logged by the app**, between the call's
  header and its tabs, so it stays in view beside the response body on every
  tab. When the app linked more than three, the section lists the first three
  and says how many there are. A link opens `request:<id>` in the log viewer,
  which lists the call's phases and every log linked to it. A session's Logs
  tab offers **Inspect this call** on these logs too, not only on network logs.
  The app sets the link with the `requestId:` parameter added in the
  `code_scout` 1.6.0 SDK.

  The log never changes the call. A 200 whose body the app could not read is
  still a complete 200, with the same status, duration and count, and it is not
  counted as failed.

- **"Call not captured."** The request id an app log names may have no network
  logs behind it. The SDK writes them at debug, so an app whose `minimumLevel`
  is info or higher keeps none, and retention can remove them. The call page and
  the inspector now say so in plain words and list the app's logs, instead of
  drawing an empty call.

### Changed

- **MCP `get_network_request` returns the app's linked logs apart from the
  call's phases**, under `linked_logs`, so an agent does not read a decode
  failure as the call's error phase. An app can link any number of logs to one
  call, so it lists the earliest 20, each truncated or omitted with a flag the
  way `search_logs` rows are, and `linked_logs_total` counts them all. When no
  phase was stored it returns empty `phases`, the linked logs and a `note`
  saying why, rather than not found. The `search_logs` syntax now says that
  `request:UUID` returns those logs as well.

### Fixed

- **Numbers in stored JSON read exactly as the app sent them.** The inspector,
  the call page, the log viewer's metadata and the MCP tools that return stored
  logs and sessions passed them through a float64, so `12.0` showed as `12` and
  an id above 2^53 came back rounded. That made a body which broke a model
  expecting a double look as if it held one.

- **The call page no longer hides a log that is not a phase.** A log with the
  call's request id and no phase was given a panel with no tab to open it, or
  opened first with no tab lit.

- **A request's own id stays on every line it logged.** At debug, the line
  that loads one call wrote the call's id under `request_id`, so searching the
  server log for a request's id missed that line. The call's id is now
  `call_request_id`.

## [1.1.0] - 2026-08-24

CodeScout can now be read by the coding agent you already have open, instead of
only by a person with the dashboard in a browser tab.

### Added

- **An MCP endpoint at `/api/mcp`.** Point an MCP client at it and your agent
  can search logs, read error groups, walk a session's timeline, inspect network
  calls, and read a paired device's local databases. It speaks the same search
  syntax as the dashboard's own search box, so a filter you can type is a query
  your agent can write. The tools are read only, which is a property of what
  they can express rather than a rule enforced somewhere: none of them takes an
  operation, a statement, or a value to write, so there is no shape a write
  could arrive in.

  The point is the handover. A tester hits a bug, taps **Copy all** in the app's
  overlay, and sends a report that carries the session id. The developer pastes
  that into their agent, which reads the whole launch back and starts from the
  same evidence rather than from a description of it.

- **Personal access tokens**, for that endpoint and for scripts. Create one
  under **Personal settings**, where it is shown exactly once and stored only as
  a SHA-256 hash, so the table is worth nothing to anyone who reads it. A token
  sees exactly the projects its owner sees, because it resolves access through
  the same code every dashboard page does. Revoking one takes effect on the next
  request. They can be given an expiry, and default to 90 days.

- **A Personal settings screen at `/account`**, separate from instance settings
  and available to every role. It holds your API tokens and your password.
  Instance settings stay where they were and stay for the people who administer
  the instance; your own things are no longer filed under the instance's.

### Changed

- **Changing your password is now part of Personal settings** rather than a page
  of its own reached from the account menu. The forced change, for an account
  still on a temporary password, is unchanged and still a page on its own,
  because an account in that state cannot reach anything else.

### Fixed

- **Text search now matches the way people search.** Searching the message text
  was case-sensitive, so `error` never found `Error: upload failed`. And the
  characters `%` and `_` were passed to the database as wildcards rather than as
  themselves, so `100%` also matched `1004` and `user_id` also matched
  `userXid`. Both fixed everywhere a search box feeds a query: messages, device
  and OS filters, network paths, and the project list. (#2, contributed by
  @huzaif-fahad)

## [1.0.0] - 2026-08-09

The first release. A self-hosted dashboard that receives batched logs and
network calls from a Flutter app, and can watch a paired device live.

### Added

- **Projects.** A create wizard that sets access as part of creating the
  project, settings in four tabs, and a secret you can reveal and rotate rather
  than one shown once and lost.
- **Accounts and access.** Three instance roles and a per-project level on top,
  decided in one place so a handler cannot forget to check. A project you cannot
  see answers 404 rather than 403. A temporary password forces a change on first
  use.
- **Log viewer.** Level toggles, tag chips with counts, time windows, keyboard
  navigation and infinite scroll. Every control is a link to the query you would
  have after clicking it, so the filter state lives in the URL and sharing a
  view is sharing a link.
- **Search that reaches the session.** `user:`, `installation:`, `app_version:`,
  `device:` and `os:` filter on facts about the launch rather than the log line.
- **Errors**, grouped by fingerprint so one bug is one row. Messages group with
  their varying parts blanked, and network failures group on method and path
  because every one of them carries the same message.
- **Network.** The three phases of a call paired back into one row, with a
  waterfall and a split inspector that swaps without reloading the page. A
  session's Network tab is the same split, filtered to that launch, so you can
  read one call after another without leaving it.
- **Sessions and Devices.** One row per launch, and launches rolled up by
  installation id.
- **Live sessions.** A paired device streams to the dashboard in real time over
  a WebSocket, fanned out to watchers over SSE. Nothing is persisted. A
  reconnecting watcher is caught up from a ring buffer, and told plainly when it
  missed more than the buffer held.
- **The live database browser.** While a device is paired, browse its SQLite
  tables, `shared_preferences` and Hive boxes, and edit one cell at a time. The
  dashboard never sends SQL: five structured operations, none of which has a
  field a statement could go in, so read-only is a property of what can be
  expressed. Nothing is browsable until the app registers it, and nothing is
  editable unless the app marks it writable.
- **Instance settings**, read live so a change applies with no restart. Display
  timezone and a 12 or 24 hour clock, retention, upload cap and a per-project
  daily log cap.
- **Volume controls.** Session sampling per project, which the SDK reads at
  startup and combines with its own rate by taking the lower of the two, so a
  server can only ever make an app quieter. A daily log cap that refuses a batch
  whole, with `Retry-After`, because the SDK has no way to say it accepted part
  of one.
- **Ingest** that reads archive entries by name rather than position, and is
  idempotent on the client's own id, so a replayed batch after a lost response
  inserts nothing and is charged for nothing.
- **Export** to CSV and JSON, log streaming over SSE, a nightly retention job,
  graceful shutdown and panic recovery.
- **Favourites.** Star a project and it pins to its own tab, per user rather
  than per project. The tab is in the query string, so it is a link you can send.
- **A time range on the overview**: the last 24 hours, 7 days or 30 days, in the
  query string so the view is a link you can send. The chart's bucket grows with
  the range, so it keeps its shape at roughly 24 to 30 columns instead of going
  from 24 bars to 168 hairlines, and the labels follow: a column is an hour at a
  day and a day at a month. Ranges longer than your retention are not offered,
  because a chart of rows the nightly job deleted reads as a collapse in traffic
  rather than as the edge of what you keep. For the same reason the day-over-day
  delta is hidden when the period it compares against is past retention.
- **A `reset-password` subcommand**, the recovery path for a locked out super
  admin. No admin outranks them and no email is ever sent, so the way back in is
  shell access to the server.
- **A version you can see.** It shows in the sidebar, in the startup log, and in
  `/healthz`, which answers it without a login because "what are you running?"
  is the first question asked about a misbehaving instance. A daily check for a
  newer release is on by default and switchable off in Settings; it asks GitHub
  and sends nothing about the instance.
- **Sessions record which version of the SDK sent them**, so an app running a
  build old enough to be missing a fix is visible instead of guessed at. It is a
  column on Sessions and on the session itself, and `sdk_version:` filters on it.
- **Logging you can configure.** `log_level` (`info` by default), `log_format`,
  and `log_file` with size, count, age and compression for rotation. Unset, it
  writes to stdout, which systemd and Docker already capture and rotate. Lines
  are readable text on a console and JSON in a file, one per request with
  `duration_ms` as a number, the response size and the caller's address. Errors
  and fatals also go to stderr when a file is configured, so a server that fails
  to start says why where you would look.

### If you have been running this from source

Three things were fixed shortly before this release. They never reached a
tagged build, but an instance built from `main` before 2026-08-07 has them.

- **The session token was written to the log** at debug on every request that
  arrived with a cookie, and debug was the hardcoded level, so the journal
  accumulated live `cs_session` values. Anyone who could read that log could
  paste one into a browser and be that user. **Rotate existing sessions if your
  log has been readable by anyone you would not hand an account to.** The fix
  stops new lines being written; it cannot retract old ones.
- **A panic sent the client its stack trace**, which handed anyone who could
  trigger one the source paths, package layout and function names of the server.
- **The response body was logged on a 4xx or 5xx**, a second way for anything
  sensitive to escape.

[1.2.0]: https://github.com/getcodescout/code_scout/releases/tag/v1.2.0
[1.1.0]: https://github.com/getcodescout/code_scout/releases/tag/v1.1.0
[1.0.0]: https://github.com/getcodescout/code_scout/releases/tag/v1.0.0
