# Porthole — project context for Claude Code

Porthole is a local web console for Apple's `container` runtime (macOS 26,
Apple silicon). A Go daemon (`portholed`) wraps the `container` CLI and serves
a REST + SSE API on `127.0.0.1:9191`. This repo's frontend is a React SPA that
renders that data. See `porthole-spec-v1.md` for the full spec; §5.4 defines the
console design and §5.2a the security model.

## Hard rules
- The frontend talks ONLY to the HTTP API. It never assumes anything about
  `container` or shells out. The API is the contract.
- Machine values (IPs, ports, container IDs, digests, subnets) are ALWAYS
  rendered in the monospace font. UI chrome is sans.
- A container's IP exists ONLY while it is running. It lives at
  `status.networks[0].ipv4Address` as CIDR (e.g. `192.168.64.2/24`) and is
  absent (empty `status.networks`) when stopped. Never render an IP for a
  stopped container; show `—`. Strip the `/NN` suffix for display.
- CPU% is computed server-side and arrives via the `stats` SSE event as
  `cpuPercent`. Do not try to derive it from raw container fields.
- No secrets, tokens, or raw stderr are logged to the browser console.

## Stack
- React 18 + TypeScript + Vite + Tailwind CSS. No component library unless a
  prompt says so. State via React hooks + a small store; no Redux.
- Single-page app, embedded into the Go binary via `go:embed` at ship time
  (a later prompt). Until then it runs on the Vite dev server proxied to
  `portholed`.

## Visual language (dense / technical — spec §5.4, direction 2)
- Tailwind theme: `font-sans` = Inter; `font-mono` = "IBM Plex Mono",
  ui-monospace. Small type (11–13px UI, 18px for hero values like an IP).
  Tight spacing. Thin 0.5px borders. Subtle surface backgrounds. Dark-mode
  supported via Tailwind `dark:`.
- Status colors (define as Tailwind theme tokens, used as small dot + text):
  running = green (#1D9E75), stopped = neutral gray, exited = neutral gray,
  starting/unhealthy = amber (#BA7517), error/danger = red. Use sparingly —
  one accent, status semantics, lots of neutral.
- The home screen is the network topology graph (host → network(s) →
  container nodes with their IPs and published-port routes), NOT a table.
  A flat list view is the fallback/escape hatch. Both render the same objects.

## API contract (base path /api)
REST (JSON):
- `GET /system/status` -> `{ apiServerRunning, cliVersion, detail? }` (never gated)
- `GET /system/version` -> `VersionEntry[]`
- `GET /system/df` -> `DiskUsage`
- `GET /containers` -> `Container[]`
- `GET /containers/{id}` -> `Container`
- `GET /networks` -> `Network[]`
- `GET /networks/{id}` -> `Network`
- `GET /stats` -> `Stats[]`
- Errors: `{ error: { kind, message, raw? } }`, kinds: `daemon_down` (503),
  `not_found` (404), `name_conflict` (409), `not_running` (409),
  `unknown_option` (400), `unknown` (500).

SSE (`GET /api/stream`, one EventSource): named events, JSON `data:` —
- `snapshot` -> `{ containers: Container[], diskUsage: DiskUsage, apiServerRunning }`
  (sent first on connect; treat as the authoritative reset of state)
- `container.upserted` -> `{ container: Container }`
- `container.removed` -> `{ id }`
- `stats` -> `{ samples: [{ id, cpuPercent, memoryPercent, memoryUsageBytes, numProcesses }] }`
- `df` -> `DiskUsage`
- `system` -> `{ apiServerRunning, detail? }` (edge-triggered on up/down change)

## Container shape (fields the UI uses)
`id`, `configuration.image.reference`, `configuration.resources.cpus`,
`configuration.resources.memoryInBytes`, `configuration.labels` (map),
`configuration.networks[].network` (membership, always present),
`configuration.publishedPorts` (array; shape still being confirmed — render
defensively), `status.state` ("running" | "stopped" | other),
`status.startedDate`, `status.networks[].ipv4Address` (CIDR, running only).

## Verify commands
- `npm run dev` (frontend) against a running `portholed` (`go run ./cmd/portholed`).
- `npm run build` must pass with no type errors before any step is "done".

## Supervision (Phase 3)
Porthole supervises containers — `container` itself has no restart policy or
health checks. Rules:
- Restart policies: `no` (default), `always`, `unless-stopped` ONLY. `on-failure`
  is intentionally NOT built — the runtime exposes no exit code, confirmed by
  capture. Never fake it.
- `unless-stopped` vs `always`: both restart a crashed/stopped container. The
  difference is INTENT — a stop the user made *through Porthole* sets
  desiredState=stopped and is NOT restarted under `unless-stopped`. Out-of-band
  stops (CLI) can't be distinguished and follow the policy default. Documented
  limitation, not a bug.
- Policy lives in Porthole SQLite keyed by container id, mirrored to a
  `porthole.restart=<policy>` label at create so it survives a DB loss. The DB is
  working truth; the label is durable backup, unioned on boot (DB wins).
- Health: HTTP or TCP probe to the container's dedicated IP directly (it's
  reachable from the host on the vmnet); fall back to 127.0.0.1:<publishedPort>
  if no IP. No exec-based health checks. States: starting → healthy/unhealthy.
- Every supervision restart goes through the same per-id mutation lock as user
  actions. Supervision never hammers: exponential backoff, max attempts, then a
  terminal gave-up state surfaced in the UI.
- id == name for named containers; unnamed containers have a UUID id and no
  name — render long/UUID ids truncated in the UI.

## Logs streaming (Phase 2)
Live log following in the inspector. Rules:
- `container logs -f <id>` follows; `container logs -n <N> <id>` gives the last
  N lines. NO `--tail` (doesn't exist), no timestamps, no since. stdout+stderr
  are MERGED — one stream, no split. Plain text, no ANSI → render in a <pre>/
  virtualized list; do NOT add xterm.js for logs.
- `logs -f` HANGS when the container stops — it never self-exits. Teardown must
  actively watch container state (reconcile hub signal) and force-kill the child
  on stop, emitting a terminal "container stopped" event. Reaping on SSE
  disconnect alone is NOT enough.
- Logs are per-container, on-demand, on a DEDICATED endpoint
  (GET /api/containers/{id}/logs), never the shared /api/stream.
- Server-side ring buffer caps memory (drop oldest + a coalesced "N dropped"
  marker); line-buffer (emit only complete lines); cap very long lines. Protect
  the producer, drop on the slow consumer — same priority as the reconcile hub.
- One child per container max (dedupe if two tabs follow the same id); cap
  concurrent streams. Stopped container → history-only view, not a spinner.

## Exec / PTY (Phase 2)
Interactive browser terminal into a running container. Rules:
- `container exec -it <id> <cmd>` + creack/pty gives a real TTY (confirmed).
  Default cmd /bin/sh, user-overridable. One bidirectional WebSocket: binary
  frames = terminal data (raw bytes both ways), text frames = JSON control
  (resize). xterm.js renders it (it DOES need a real terminal emulator, unlike
  logs' <pre>).
- SECURITY (highest-risk feature): the WS upgrade MUST pass the same Origin/Host
  browser-guard as REST mutations — a cross-origin upgrade is WebSocket CSRF and
  yields a remote root shell. The guard runs on the upgrade request (it's an HTTP
  GET first); reject foreign Origin. Exec is why the bind-guard refuses
  non-loopback without auth.
- NO reconnect: a dropped WS ends the session (shell state is gone). UI shows
  "session ended" + manual new-session, never auto-reconnect. Inverse of SSE.
- exec exits CLEANLY on container stop (rc 137) — does not hang like logs -f.
  Teardown: natural exit + ctx-kill (WS/tab close) + ping/pong (half-open). The
  hub WatchStopped reuse is OPTIONAL, not load-bearing.
- Exec is NOT deduped — each session is an independent shell (opposite of logs
  fan-out). Cap concurrent sessions; two tabs = two PTYs + two children.
- Reap BOTH the PTY (fds) and the child process on teardown. Set an initial size
  (80×24) + send an initial resize at spawn to skip the 0×0 window.
- Block exec on a stopped container (runtime errors "is not running"); the UI
  disables the action when stopped.

## Stacks (Phase 4, v1 — non-destructive)
Compose-style multi-container orchestration. Rules:
- Parse a docker-compose SUBSET → container run. Reject build/profiles/deploy/
  extends/configs/secrets with a validation report; never silently ignore.
- Membership = porthole.stack=<name> + porthole.service=<svc> labels at create;
  re-discover members from `container ls`, never trust the DB alone. The DB
  holds the file (desired); labels prove membership; `ls` is observed truth.
- restart: → supervision labels (no/always/unless-stopped). Stacks owns group
  shape (up/down/reconcile); supervision owns per-container liveness. Names are
  namespaced <stack>-<service>; logical name in porthole.service.
- Each stack gets its own network (container network create — isolates, own
  subnet). DNS does NOT resolve between containers, so v1 is IP-based (topology
  surfaces service IPs). Discovery injection (/etc/hosts) is v2.
- v1 is NON-DESTRUCTIVE: up/down (keep volumes)/restart + drift DETECTION (show
  the plan/diff). Recreate-on-change is DETECTED + SHOWN but NOT applied — that's
  v2 (destructive: downtime + data loss). down keeps named volumes by default.
- Per-stack lock (two ups on one stack serialize). Partial-up failure → report
  degraded, do NOT auto-rollback; re-up is idempotent.

## Create / Run container (Phase 5)
Standalone "Run container" flow. Rules:
- Reuse the Stacks RunSpec (one container, no stack labels) — the run mapping is
  already proven. Create = run (create+start); `container create` (no start) is
  a future option.
- Run AUTO-PULLS a missing image and BLOCKS, emitting a 6-phase counter on stderr
  (`[N/6] <phase> [Xs]`: fetch image / unpack image / fetch kernel / fetch init /
  unpack init / start). Create streams that progress over SSE (spawn with
  --progress plain, parse phases) — NEVER a frozen sync dialog. Same
  spawn-stream-reap pattern as logs; reap the child on disconnect. Progress is a
  phase stepper, not a % bar.
- Restart policy at create → porthole.restart label + record desired=running in
  the supervisor store (mediated start). Health at create → porthole.health.*.
- Image-not-found surfaces as a 401 pull failure (image_pull_failed →
  "image not found or inaccessible"), NOT not_found. Name conflict → 409.
- Bind mounts allowed, labelled host-path (type.virtiofs); named volumes
  auto-create (type.volume). Resources: --cpus N, --memory <n>m (MiB).
- Create is a mutation: browser-guard + bootstrap gate + typed errors.

## Resources / Disk (Phase 6)
Manage images, volumes, networks + reclaim disk. Rules:
- Image DELETE is never refused by the runtime (deletes even under a running
  container). So image in-use is ADVISORY: warn before deleting, no typed error
  to surface, --force only ignores not-found. Volume DELETE IS refused when in use
  (running OR stopped) → typed volume_in_use error ("volume '<n>' is currently in
  use").
- Anonymous volumes are BARE UUID names (not anon-<uuid>). Orphan = UUID-shaped
  name + zero container references; the prune PREVIEW is the backstop for a user
  who named a volume a UUID.
- Per-volume sizeInBytes is ALLOCATED/sparse, NOT usage — never show it as disk
  used. Real reclaim comes from df + the prune "Reclaimed X in disk space" line.
- Protected networks: label com.apple.container.resource.role=builtin → show,
  non-removable. volume inspect has NO --format (use the ls JSON).
- Prune is preview-then-apply (like Stacks drift): show exactly what goes +
  estimated reclaim, apply on confirm. Mutations gated + guarded + typed.
- Reuse: df stream (summary), Stacks net engine, create pull-progress. A
  resourceEpoch SSE nudge after mutations; lists refresh on demand, not a new poll.

## Registry login (Phase 7 / v2) — FIRST secret-handling feature
Lets the user authenticate to a registry from the UI so private images pull.
container owns the credential; Porthole is a safe front-end. INVARIANTS:
- Token over STDIN only (container registry login --password-stdin -u <user>
  <host>) — NEVER as an argument (process list / history / logs would leak it).
  The engine takes the token as an io.Reader piped to stdin, never a string arg.
- Porthole NEVER stores the token — not in SQLite, not a file, not a cache. Hold
  it only long enough to pipe to the child's stdin, then drop it. The credential
  lives in container's own store (registry list shows it persisted).
- Token NEVER in a log line, response body, or error message. The login handler
  must not log its request body; the error is a FIXED friendly message, never
  echoed stderr (which could contain supplied input). Raw is scrubbed (empty).
- registry list --format json → [{id,name(host),username,creationDate,
  modificationDate,labels}] — host+username only, no secret. Read for STATE.
- registry logout <host> drops the login. Host normalize: docker.io →
  registry-1.docker.io (what name/id report); display registry-1.docker.io as
  "Docker Hub".
- SSO/2FA accounts need a Personal Access Token used AS the password → the UI
  field is "password or access token" with an SSO hint + token-page link.
- Login is a mutation: gated + browser-guarded like every write, PLUS the
  never-log/store/echo rules. New Settings view hosts it; create-error nudges to it.

## Keychain-stall handling (Phase 7b / v2) — completes registry login
A logged-in user's first PRIVATE pull hangs: the container-core-images helper
reads the credential from the macOS keychain and macOS prompts the SecurityAgent;
headless portholed can't show the dialog, so the pull sits at [0/N] forever.
- Porthole does NOT fix the keychain (can't without the login password + touching
  the credential — breaks the invariant). It detects the stall, kills the pull,
  and shows the one-time fix: run `container image pull <ref>` in Terminal once,
  click "Always Allow"; thereafter headless pulls read silently (ACL grant is on
  the keychain item, keyed to the helper's signature, not the caller).
- Stall SIGNATURE = still at initial phase [0/N] + no first progress line after a
  timeout. A slow mid-pull (early progress then quiet) is NOT a stall — do not trip
  on "any gap," only on "no progress while still at phase 0." Hedged wording
  ("appears stalled / likely keychain"), never "failed."
- Pieces: server watchdog → typed pull_stalled event; create-form actionable
  message with the real image ref + Always Allow + retry; proactive post-login
  Settings hint; docs. Reuse the create stream's context-cancel for teardown.
- Configurable: PORTHOLE_PULL_STALL_SECS (default ~25).

## Service discovery (Phase 8 / v2) — stack name resolution via /etc/hosts
Stack services resolve peers by name. Captures forced exec-based /etc/hosts
injection: no --add-host, no DNS A-records (NXDOMAIN even with a domain), no
--hostname. /etc/hosts is WIPED to self on every start; peer IPs churn. So:
- Re-inject the FULL peer set on EVERY member start (file is self-only at boot),
  and re-inject the started member's new IP into every running peer.
- Idempotent marked block: "# >>> porthole-managed (stack: X)" … "# <<<" —
  strip-and-replace wholesale each time (no append; no growth; no stale lines).
  Write both bare name (api) and namespaced (stack-api). Skip self.
- Convergence loop driven by the reconcile hub's start/stop signals (same source
  logs/supervision use). Settles as members come up. Debounce per-member churn.
- Best-effort + idempotent retry on next cycle; NEVER fatal, never wedges a stack.
  Single atomic write (cat > /etc/hosts of the full merged file). Share idlock so
  injection never races a supervisor restart on the same container.
- Same-stack peers only (porthole.stack/service labels). Opt-in per stack
  (porthole.discovery=on), OFF by default. Toggle off → strip the managed block.
- Pure core: computeHostsBlock / mergeHosts / planInjections (table-tested). Exec
  + hub subscription + debounce + idlock are thin I/O around it.

## Health-at-create + richer create flags (Phase 9 / v2)
Completes the create form. Additive. Key points:
- Health is NOT a container run flag (no --health-* exists). Health-at-create
  wires to Porthole's supervision prober the SAME way restart-at-create does:
  write the health label/supervision record at create → the existing HTTP/TCP
  prober adopts it from birth. Reuse the inspector's EXACT health config shape —
  one health model, two entry points (create + inspector). Keep health OUT of
  RunSpec/toArgs. (Backend already maps createSpec.Health → porthole.health.*
  labels, which healthFromLabels adopts — same path as the restart label.)
- New RunSpec/toArgs flags: --init, --read-only, --entrypoint, --cap-add/--cap-drop
  (repeated), --tmpfs (repeated), --shm-size. user/workdir already exist in RunSpec.
- Form layering (don't make a wall): health next to restart (both feed
  supervision), user/workdir near command; init/read-only/entrypoint/caps/tmpfs/
  shm behind the existing Advanced disclosure. Repeatable rows reuse the
  ports/env/volumes "+ add" pattern. Plain-nginx path shows none of the new
  advanced fields unless opened.
- Excluded: --rm, --mount, --dns*, arch/os/platform/expert flags.

## Destructive drift remediation (Phase 10 / v2) — completes Stacks
Applies the recreate the planner flags (a service whose spec changed). Strategy
forced by capture: id==name strict + NO rename → create-then-swap can't end clean
→ stop-remove-create + spec-snapshot ROLLBACK.
- Sequence (per drifted service): snapshot old RunSpec → stop → remove →
  create(new spec) → start → verify. On create/start failure: re-create from the
  snapshot, start, and surface LOUDLY "recreate of <svc> failed — restored
  previous". If rollback ALSO fails: critical state, service named DOWN with the
  snapshot shown. Never silent.
- Snapshot source (§9b, Phase 10b): the AUTHORITATIVE RunSpec is persisted in the
  stacks store at create + recreate, keyed by container name — rollback uses it for
  a BYTE-PERFECT restore (keeps --entrypoint/--shm-size/--tmpfs the observed object
  can't recover). A container with no stored spec (pre-feature, or non-stack) falls
  back to reconstructing from `inspect` — additive, never worse than before.
- Hold the shared idlock across the WHOLE sequence (not per-step) so supervisor
  (won't see it "missing" and restart), discovery (won't inject mid-remove), and
  other mutations can't race the gone-window. After release + new container up,
  discovery re-injects (new IP/startedDate, DISC3) and supervision re-adopts (new
  RunSpec labels) automatically.
- Named volumes survive recreate (preserve); anonymous volumes orphan (warn in
  confirm). Rolling: one service at a time, each isolated — a rollback in one
  doesn't cascade. Explicit apply only, never auto. Recreate only, not orphan.

---

# context-mode — MANDATORY routing rules

You have context-mode MCP tools available. These rules are NOT optional — they protect your context window from flooding. A single unrouted command can dump 56 KB into context and waste the entire session.

## BLOCKED commands — do NOT attempt these

### curl / wget — BLOCKED
Any Bash command containing `curl` or `wget` is intercepted and replaced with an error message. Do NOT retry.
Instead use:
- `ctx_fetch_and_index(url, source)` to fetch and index web pages
- `ctx_execute(language: "javascript", code: "const r = await fetch(...)")` to run HTTP calls in sandbox

### Inline HTTP — BLOCKED
Any Bash command containing `fetch('http`, `requests.get(`, `requests.post(`, `http.get(`, or `http.request(` is intercepted and replaced with an error message. Do NOT retry with Bash.
Instead use:
- `ctx_execute(language, code)` to run HTTP calls in sandbox — only stdout enters context

### WebFetch — BLOCKED
WebFetch calls are denied entirely. The URL is extracted and you are told to use `ctx_fetch_and_index` instead.
Instead use:
- `ctx_fetch_and_index(url, source)` then `ctx_search(queries)` to query the indexed content

## REDIRECTED tools — use sandbox equivalents

### Bash (>20 lines output)
Bash is ONLY for: `git`, `mkdir`, `rm`, `mv`, `cd`, `ls`, `npm install`, `pip install`, and other short-output commands.
For everything else, use:
- `ctx_batch_execute(commands, queries)` — run multiple commands + search in ONE call
- `ctx_execute(language: "shell", code: "...")` — run in sandbox, only stdout enters context

### Read (for analysis)
If you are reading a file to **Edit** it → Read is correct (Edit needs content in context).
If you are reading to **analyze, explore, or summarize** → use `ctx_execute_file(path, language, code)` instead. Only your printed summary enters context. The raw file content stays in the sandbox.

### Grep (large results)
Grep results can flood context. Use `ctx_execute(language: "shell", code: "grep ...")` to run searches in sandbox. Only your printed summary enters context.

## Tool selection hierarchy

1. **GATHER**: `ctx_batch_execute(commands, queries)` — Primary tool. Runs all commands, auto-indexes output, returns search results. ONE call replaces 30+ individual calls.
2. **FOLLOW-UP**: `ctx_search(queries: ["q1", "q2", ...])` — Query indexed content. Pass ALL questions as array in ONE call.
3. **PROCESSING**: `ctx_execute(language, code)` | `ctx_execute_file(path, language, code)` — Sandbox execution. Only stdout enters context.
4. **WEB**: `ctx_fetch_and_index(url, source)` then `ctx_search(queries)` — Fetch, chunk, index, query. Raw HTML never enters context.
5. **INDEX**: `ctx_index(content, source)` — Store content in FTS5 knowledge base for later search.

## Subagent routing

When spawning subagents (Agent/Task tool), the routing block is automatically injected into their prompt. Bash-type subagents are upgraded to general-purpose so they have access to MCP tools. You do NOT need to manually instruct subagents about context-mode.

## Output constraints

- Keep responses under 500 words.
- Write artifacts (code, configs, PRDs) to FILES — never return them as inline text. Return only: file path + 1-line description.
- When indexing content, use descriptive source labels so others can `ctx_search(source: "label")` later.

## ctx commands

| Command | Action |
|---------|--------|
| `ctx stats` | Call the `ctx_stats` MCP tool and display the full output verbatim |
| `ctx doctor` | Call the `ctx_doctor` MCP tool, run the returned shell command, display as checklist |
| `ctx upgrade` | Call the `ctx_upgrade` MCP tool, run the returned shell command, display as checklist |
