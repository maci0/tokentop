# toktop threat model

Living document for the security owner: the whole attack surface on one screen,
with file references so each claim can be re-verified against code. Individual
vulnerabilities and their fixes belong to sec-review; this file records where
they live and what already stands in their way.

- **Last reviewed:** 2026-08-26 (commit d9d478d; re-verified against
  498454e..d9d478d, which added the ingest Origin refusal and the remote
  port-range hardening recorded below as M21/M22)
- **Owner:** none assigned in this repository
- **Review cadence:** none scheduled organizationally; re-run whenever an entry
  point, auth path, or bind default changes

Scope: the `toktop` CLI (single static Go binary), its self-update channel,
and deployment artifacts in this repository (GitHub Actions workflows, Makefile
release targets, the static site worker at site/worker.js). Out of scope: the
`gauntlet` tool that shares this repo's in-repo `agentusage` package
(deps-review owns the gauntlet side; agentwatch consumes the package via
internal/agentwatch/agentwatch.go:27,75-88).

## Risk-ranked summary

| # | Risk | Boundary | Where | State |
|---|------|----------|-------|-------|
| 1 | Ingest endpoint accepts unauthenticated events from any local process (any network peer if bound non-loopback via `--ingest`); forged telemetry renders as real agents | local processes -> ingest, network -> ingest | internal/ingest/server.go:41-59, cmd/toktop/main.go:55,262-289 | No authentication; browser-driven forgery, DoS, injection, and timestamp forgery mitigated (see M3-M7, M21) |
| 2 | Trust-on-first-use accepts a first-contact MITM by design; only key *changes* are refused | operator -> ssh target | internal/remote/knownhosts.go:50-51 | Documented residual risk (README "Auth" section) |
| 3 | Self-update installs whatever binary the named GitHub repo published: integrity rests on the release's own checksums.txt over TLS; no external signature exists | runtime -> update channel | internal/selfupdate/selfupdate.go:133-195, .github/workflows/release.yml | Checksum + size verification present; signing absent |
| 4 | Hot-reload re-execs whatever binary occupies the exe path when its identity changes; PATH-based vendor CLI lookup executes tools from `$PATH` | build -> runtime, host -> process | internal/selfreload/exec_unix.go:15, internal/gpu/gpu.go:46-55 | Gated by filesystem permissions / same-user requirement; `--no-hot-reload` exists |
| 5 | No audit trail: ingest requests are not logged, nothing is persisted, so a poisoning or token-capture incident cannot be reconstructed after the fact | response readiness | internal/ingest/server.go (no logging anywhere in handler) | Gap (note only here; o11y-review owns log structure) |

Resolved since 2026-08-25: risk 1 of the previous ranking ("bearer token sent
to every probed endpoint") is closed by origin-scoped token application
(commit 21e3feb); see mitigations M1. Nothing in this table is demonstrated by
attacking anything; every claim cites code read at review time.

## Assets

What is worth stealing, corrupting, or denying:

- **Engine credentials**: one process-wide bearer token
  (internal/bearer/bearer.go:20-25), sourced from `--bearer`,
  `OMNIROUTE_API_KEY`, or `TOKTOP_BEARER` (cmd/toktop/main.go:115-124).
  Grants API access to gateways like OmniRoute.
- **GitHub credential**: optional `GITHUB_TOKEN`, read from the environment
  during `toktop update` and sent only to api.github.com
  (internal/selfupdate/selfupdate.go:85-87). Rate-limit relief, not a secret
  with broad scope - but it leaves the host on every update check that sets it.
- **SSH credentials**: private keys read from `--ssh-key`, `~/.ssh/config`
  IdentityFile, or `~/.ssh/id_*` defaults (internal/remote/auth.go:18-47), the
  ssh-agent socket (auth.go:163-170), and the password from
  `TOKTOP_SSH_PASSWORD` or the terminal prompt (auth.go:91-113).
- **Host-key pin store**: `os.UserConfigDir()/toktop/known_hosts`
  (internal/remote/knownhosts.go:16-22). Corrupting it enables a forced
  re-TOFU.
- **Binary integrity**: the running executable is replaceable by design twice
  over - hot-reload (internal/selfreload/exec_unix.go:15) and
  `toktop update` (cmd/toktop/update.go:80). Whoever controls either channel
  controls code execution as the user.
- **Dashboard integrity**: what the operator sees drives triage decisions.
  Poisoned rows are the main prize of the ingest endpoint.
- **Operator terminal integrity**: toktop renders attacker-shaped text
  (engine model names, remote vitals, event fields) into a TTY; escape-sequence
  injection would hijack clipboard/cursor/title.
- **Monitoring availability**: the dashboard session itself (low value, bounded
  blast radius).
- **Information disclosure via display**: remote host CPU/OS/kernel/GPU
  inventory, engine/model lists, and agent working directories (last two path
  components in event notes, internal/agentwatch/agentwatch.go:244-252) appear
  on screen and in scrollback; screen shares and captures leak them.

## Entry points

Every externally reachable input, with its code location:

1. **Ingest HTTP server** (on by default): `POST /v1/events` (single JSON or
   NDJSON stream), `GET /v1/events`, `GET /healthz`
   (internal/ingest/server.go:48-54). Binds `127.0.0.1:8420` unless `--ingest`
   says otherwise (cmd/toktop/main.go:55); any address is accepted, including
   routable interfaces. A routable bind prints a startup warning naming the
   unauthenticated exposure (main.go:275-277, routableBind :313-323). Runs in
   demo mode too.
2. **CLI arguments**: top-level flags including `--bearer` (secret),
   `--ssh-key`, `--add URL` (repeatable), `--ingest ADDR`
   (cmd/toktop/main.go:50-73); positional `ssh://[user@]host[:port]` targets
   (:126-135); and the `update` subcommand with `--check` and `--repo
   owner/name` (cmd/toktop/update.go:42-66). `--repo` points the update
   channel at any GitHub repository. The live dashboard additionally refuses
   to start when stdout is not a terminal (main.go:95-101), so piped
   alt-screen bytes cannot end up in a capture the operator did not ask for;
   `--once` is the non-TTY path.
3. **Environment variables**: secrets `OMNIROUTE_API_KEY`,
   `TOKTOP_BEARER`, `TOKTOP_SSH_PASSWORD`, `GITHUB_TOKEN`; plus
   `SSH_AUTH_SOCK`, `TOKTOP_COLUMNS/LINES` (cmd/toktop/main.go:115-124;
   internal/remote/auth.go:101,163; internal/selfupdate/selfupdate.go:85-87;
   cmd/toktop/main.go:482-495,521-539,553-566).
4. **Self-update network fetches** (outbound HTTPS): latest-release lookup on
   api.github.com, then download of the checksums archive and platform asset
   named by that response (internal/selfupdate/selfupdate.go:75-107,152-163,
   218-258). The asset is verified against the downloaded checksums and size-
   capped before anything is renamed over the running binary (:165-195).
5. **SSH client sessions** (outbound): shell scripts executed on the remote
   for discovery and vitals (internal/remote/discover.go:61-86,119-148;
   internal/remote/stats.go:39-64); all returned text is parsed locally, and
   discovery ports are parsed as 16-bit with zero rejected
   (discover.go:93-116). Local relay listeners bound to `127.0.0.1:0` per
   forwarded port (internal/remote/client.go:320).
6. **Engine HTTP polling** (outbound GET/POST): startup probe of ~18 well-known
   localhost ports (internal/provider/discover.go:22-41), process-derived
   candidates (discover.go:91-105), operator-supplied `--add` URLs, and remote
   ports discovered over ssh (cmd/toktop/main.go:586-654). Probes POST small
   generations to engines (internal/probe/probe.go).
7. **Agent transcript reading** (local disk): `/proc` walk finds coding-agent
   processes; their transcript files are read every second via the in-repo
   `agentusage` package (internal/agentwatch/agentwatch.go:93-111). Agent
   definitions, including transcript paths, load once at startup from
   `~/.gauntlet/agents.json`; a malformed file warns on stderr and leaves
   only the built-in set (cmd/toktop/main.go:250-252,573-581).
8. **Config files read at startup**: `~/.ssh/config` (HostName/User/Port/
   IdentityFile override target fields, internal/remote/target.go:25-46,83-88)
   and the known_hosts store (knownhosts.go:55-74).
9. **Self hot-reload**: polls the running executable's stat identity and
   restarts into it when changed (internal/selfreload/selfreload.go,
   exec_unix.go:15; cmd/toktop/main.go:327-366). Default-on in interactive
   runs; `--no-hot-reload` disables.
10. **Local system introspection**: `/proc` and sysctl reads
    (internal/procs/, internal/sysmon/), vendor CLIs executed from `$PATH`:
    nvidia-smi, rocm-smi, xpu-smi (internal/gpu/gpu.go:72-97), system_profiler
    (internal/gpu/gpu_darwin.go:53), ps (procs_darwin.go:21), a PowerShell CIM
    query (procs_windows.go:30).

Deployment surface:

- GitHub Actions CI runs gofmt/vet/govulncheck/race tests on pushes and PRs
  (.github/workflows/ci.yml, actions pinned by SHA); Dependabot updates
  modules and actions (.github/dependabot.yml); tag pushes build release
  binaries for five platforms plus a CycloneDX SBOM
  (.github/workflows/release.yml, Makefile `release`/`sbom`). Release
  artifacts ship SHA-256 checksums only; no signature step exists.
- The marketing site is a single Cloudflare Worker serving one static page
  from an embedded string (site/worker.js:7-145): GET/HEAD only (:226-228), a
  `/health` route (:229-235), ETag revalidation with a weak validator
  (:148-174,237-248), gzip content negotiation compressed once per isolate
  (:176-196,250-252) keyed by `Vary: Accept-Encoding` on every page response
  (:206,208-213), and hardening headers (nosniff, HSTS, referrer-policy, CSP
  with `default-src 'none'`) on every response (:215-221). The only request
  bytes it inspects are the method, path, If-None-Match, and Accept-Encoding
  headers, compared as strings; nothing is stored, echoed into the page, or
  forwarded anywhere.

## Trust boundaries and data flow

- **B1: local processes -> ingest server.** Any process running as any local
  user who can reach the socket can POST events. There is no named
  authentication or validation-of-origin point; sanitization happens
  (server.go:193-225) and browser-driven requests are refused by the
  Origin-header check (server.go:123-132), but attribution does not exist.
  When `--ingest` binds a non-loopback address this boundary widens to the
  network; the widening is announced at startup (main.go:275-277) but not
  prevented.
- **B2: engine HTTP responses -> toktop.** Whatever answers on a scanned
  port, a `--add` URL, or a forwarded remote port is treated as an engine:
  its JSON, Prometheus text, version strings, and error bodies flow into
  parsing and rendering. Validation points: body caps (provider.go:68,90),
  non-finite rejection (provider.go:191,206-208 and family-sum overflow guard
  :182-185), render-time sanitization (call sites listed in mitigation M2).
- **B3: remote ssh host -> toktop.** A compromised or hostile ssh target
  controls every byte returned by discovery and vitals scripts: `/proc/net/tcp`
  text, cmdline dumps, loadavg/meminfo/CPU/OS/kernel strings, nvidia-smi CSV or
  rocm-smi JSON (stats.go:39-64, discover.go:56-148). Authentication is the ssh
  handshake itself; after that the data crosses unvalidated except by parsers
  (numbers parse-or-zero, e.g. stats.go:163, gpu.go:181-193; ports parse as
  16-bit with zero rejected, discover.go:93-116) and the renderer's sanitizer.
- **B4: secrets -> code.** Bearer token: enters via argv or env
  (main.go:115-124), lives in a package var (bearer.go:20-25), leaves in the
  `Authorization` header of requests bound for origins admitted by `Allow` -
  populated solely from operator-named `--add` URLs (main.go:166);
  discovery scans, forwarded remote engines, and probes elsewhere never carry
  it (bearer.go:54-70 gates Apply at every call site: discover.go:72,289;
  provider.go:59,81; probe.go:242). It is never written to disk or logs.
  SSH password: env or TTY prompt (auth.go:91-113), held in memory, used only
  for password and keyboard-interactive mechanisms. Keys: read from disk or
  agent, sign locally. `GITHUB_TOKEN`: env, sent only to api.github.com
  (selfupdate.go:85-87). Rotation points: none; all credentials are static for
  the life of the run.
- **B5: build -> runtime.** Two channels replace the executing image:
  hot-reload trusts that whoever can change the exe file is authorized to run
  code as this user (selfreload.go:20-43 fires on stat-identity change), and
  `toktop update` trusts the named GitHub repo's release contents delivered
  over TLS (selfupdate.go). Both end in execution of the replaced binary.
- **B6: user config -> runtime.** `~/.ssh/config` steers where connections go
  and which identity is offered (target.go:25-46); `~/.gauntlet/agents.json`
  defines which processes count as agents and which files are read
  (cmd/toktop/main.go:573-581); `PATH` decides which vendor CLIs execute
  (gpu.go:46-55). All three are same-user-writable inputs treated as trusted.

Privilege transitions: toktop gains no privileges at runtime (no setuid,
no sudo); the ssh connection is the one place code acts with authority beyond
the local process - it executes shell commands on the remote under the target
account (client.go:255-286).

## Threats per boundary (STRIDE, concrete)

**B1 (and widened B1 via `--ingest`):**
- *Spoofing*: forge agent rows ("claude", "codex") with arbitrary throughput,
  models, and notes by POSTing to /v1/events; no origin auth exists
  (server.go:122-232). Renders beside genuine agentwatch data. The one
  sender class refused outright is the browser: a POST carrying an `Origin`
  header gets 403 (server.go:123-132), closing cross-site forgery from web
  pages the operator visits. Forged future timestamps are clamped to arrival
  time beyond a 2-minute skew (server.go:88,220-225), so the "● live" marker
  cannot be pinned by a claimed far-future stamp.
- *Repudiation*: source address is never recorded, so even loopback forgery
  cannot be attributed afterward.
- *Information disclosure*: none beyond presence (`/healthz` answers any
  requester, server.go:50-54).
- *Denial of service*: mitigated (see M3-M5). Residual: no per-peer
  connection quota; each open POST holds an fd plus goroutine until the byte
  cap, body-idle timeout (1m), or absolute lifetime (10m) reaps it
  (server.go:79,98-120) - a local flood of stalled POSTs is bounded but
  nonzero.
- *Elevation of privilege*: none; event content reaches only parsing and
  rendering.

**B2:**
- *Spoofing/tampering*: a hostile listener on a scanned port can pose as an
  engine and feed fake metrics, versions, and model lists (display-only
  effect). Fingerprinting order in identify (discover.go:175-229) decides the
  label; mislabeling is possible, impact is a wrong row.
- *Information disclosure*: scoped since commit 21e3feb - scanned ports and
  forwarded remotes receive no Authorization header because they were never
  Allow-ed (bearer.go:54-70). Residual: the token is still exposed to whatever
  listens on an origin the operator explicitly `--add`ed; that is inherent to
  attaching to an endpoint, not a defect, but it means `--add` trust equals
  credential disclosure to that origin.
- *Denial of service*: slow responses bounded by scanTimeout 700ms /
  PollTimeout 1.5s / probe timeout 30s; bodies capped (M8).
- *Elevation*: none; response bytes never reach execution or unsanitized
  output.

**B3:**
- *Spoofing*: TOFU pins nothing on first contact; a MITM present before the
  first connect is pinned as genuine instead (knownhosts.go:50-51). Key change
  refusal is loud and dual-fingerprinted (knownhosts.go:43-48).
- *Tampering/information disclosure*: hostile remote shapes vitals, GPU names,
  and engine lists shown locally; sanitized at render (M2), numbers parse
  defensively - residual risk is misleading content, not injection.
- *Denial of service*: remote can stall each poll up to runTimeout 15s
  (client.go:25) and the handshake up to bannerTimeout 15s
  (client.go:82); keepalive turns silent death into a closed connection
  within ~45s (client.go:188-209).
- *Elevation*: remote input never becomes local command text; remote-side
  scripts interpolate only locally generated integers (probeScript,
  discover.go:119-133), so no injection path into the remote shell either.
  Discovery output cannot plant impossible tunnels either: listening ports
  parse as 16-bit with zero rejected (discover.go:93-116, fuzz-pinned by
  FuzzParseDiscoveryOutput), so a hostile /proc dump cannot name an out-of-
  range forward target.

**B4 (secrets):**
- *Information disclosure*: token passed via `--bearer` is visible in process
  listings - README documents the env fallback for exactly this reason;
  `TOKTOP_SSH_PASSWORD` in the environment is readable by same-user processes
  and inherited by children (auth.go:101); `GITHUB_TOKEN` reaches api.github.com
  on every `toktop update` check when set (selfupdate.go:85-87).
- *Tampering*: known_hosts store is plain text in the user config dir, mode
  0600 in a 0700 dir (knownhosts.go:76-85); a same-user writer can reset pins.

**B5 (build/runtime, both replacement channels):**
- *Elevation of privilege*: swapping the exe file converts hot-reload into
  arbitrary code execution as the running user (exec_unix.go:15); substituting
  `nvidia-smi` et al. via `PATH` runs attacker code with user privileges
  (gpu.go:46-55). Both require write access the user already controls; they
  matter when toktop runs with more authority than the attacker has (none
  today - noted for future privilege changes).
- *Spoofing/tampering* (`toktop update`): the release lookup and downloads
  ride TLS, and the asset must match the release's own checksums.txt
  (selfupdate.go:152-183), so a network attacker cannot substitute a binary.
  The trust anchor is the repo itself: whoever controls the repo (or its
  release pipeline) ships a binary that verifies against its own checksums.
  No external signature (Sigstore/GPG) closes that gap today; summary risk 3.
- *Denial of service*: downloads capped at 256 MiB asset / 1 MiB checksums
  (selfupdate.go:40,152,249-255); a truncated or hostile mirror fails
  verification and leaves the running binary untouched (:180-182).

**B6 (user configs):**
- *Elevation of privilege*: `--repo owner/name` redirects the update channel
  to an arbitrary public repo; combined with the self-published-checksum
  trust anchor above, installing from a hostile repo is operator-invoked
  arbitrary code installation. `agents.json` chooses which files toktop reads
  every second; a same-user writer can point it at anything readable.

## Existing mitigations map

Controls verified in code, with the threats they cover:

| Control | Covers | Location |
|---|---|---|
| M1: Origin-scoped bearer application - token attached only to `Allow`-admitted origins, populated exclusively from operator `--add` URLs | Credential harvesting by scanned ports, forwarded remotes, and probes (B2/B4 disclosure; previous summary risk 1, fixed in commit 21e3feb) | internal/bearer/bearer.go:44-97; cmd/toktop/main.go:166; call sites discover.go:72,289; provider.go:59,81; probe.go:242; tests internal/bearer/bearer_test.go |
| M2: Terminal escape/control-char sanitizer applied both at ingest and at render time (C0/C1 including UTF-8-encoded C1) | OSC/CSI clipboard-cursor-title injection from engines, remotes, and events (asset: terminal integrity) | internal/core/sanitize.go:16-101; ingest side server.go:193-203; render side ui.go:268,624-650,674,753-783,854,897,940-957,1046-1057; internal/ui/format.go:128-129; internal/ui/agents.go:91-113 |
| M3: Ingest body cap 1 MiB + MaxBytesReader | unbounded upload into decode loop (B1 DoS) | server.go:79,136 |
| M4: Ingest read deadlines: 10 min absolute lifetime, 1 min idle extension, 5 s header timeout, 2 min idle reap | slowloris/drip DoS (B1) | server.go:39,57-58,98-101,107-120,133-136 |
| M5: Event field clamps (agent 64, model 128, note 512, kind 24 runes) + retention caps (64 agents, 128 probes per snapshot) | memory pinning via oversized or numerous events (B1 DoS) | server.go:198-219; core.HistoryLen constants internal/core/core.go:6-13; collector.go:403-405,415-417 |
| M6: Event timestamp skew clamp: stamps >2 min in the future reset to arrival time | forged-future stamps pinning the live marker and feed ordering (B1 spoofing) | server.go:88,220-225 |
| M7: Negative token counts clamped to zero; unknown kinds defaulted | junk values entering retained state (B1 tampering) | server.go:204-219 |
| M8: Engine response caps: 4 MiB JSON, 8 MiB text, 256-rune error snippets | memory blowup and log flooding from hostile engines (B2 DoS/disclosure) | provider/provider.go:68,90; httperr/httperr.go:17,21-38 |
| M9: Non-finite rejection in metrics (per-value and family-sum overflow guard) and vendor CSV/JSON coercion | poisoned counters/rates propagating through history (B2 tampering) | provider.go:182-185,191,206-208; gpu.go:181-193; collector counter-reset clamp collector.go:322 |
| M10: Probe generation ceiling 512 tokens; fixed small prompt | probes becoming compute-amplification attacks against engines (B2 DoS) | probe.go:24-31 |
| M11: Poll/scan/probe timeouts (700ms/1.5s/30s) + context-bounded requests | hung-engine DoS (B2/B3) | discover.go:20; provider.go:28; probe.go:20 |
| M12: TOFU host-key store with loud change refusal, 0600 file in 0700 dir | silent MITM after first contact (B3 spoofing) | knownhosts.go:27-53,76-85 |
| M13: Banner deadline lifted only on complete version line; 15s command timeout; keepalive with bounded probe waits | trickle/silent-peer hangs (B3 DoS) | client.go:25,82-109,188-209 |
| M14: Local-only defaults: forward listeners on 127.0.0.1, ingest on 127.0.0.1:8420 | accidental network exposure (B1 widening) | client.go:320; main.go:55 |
| M15: Routable-bind warning at ingest startup | silent widening of B1 to the network (visibility control; the widening itself remains possible) | main.go:275-277,313-323 |
| M16: Remote shell scripts: static bodies, only locally generated integers interpolated; no secret material sent to remote scripts | command injection into remote shell (B3 elevation) | discover.go:86,119-148; stats.go:39-64 |
| M17: Password prompt gated on TTY; encrypted keys skipped with guidance | credential handling in headless runs (B4) | auth.go:41,103-107 |
| M18: Self-update verification: refuses without checksums asset, SHA-256 match required before rename, 256 MiB size cap, temp-file-plus-atomic-rename install | tampered/truncated/unbounded downloads reaching execution (B5) | selfupdate.go:133-195,249-255,201-216 |
| M19: Flag validation exits 2; set-but-invalid `TOKTOP_COLUMNS`/`TOKTOP_LINES` exit 2 under `--once` (and are named as ignored without it); non-TTY stdout aborts the live dashboard; malformed `~/.gauntlet/agents.json` warned instead of swallowed; unknown `TOKTOP_*` env warned | misconfiguration acting as silent security-relevant behavior change | main.go:86-101,482-495,497-508,521-539,553-566,573-581 |
| M20: Supply chain: govulncheck in CI, Dependabot, SHA-pinned workflow actions, SBOM in releases | vulnerable-dependency drift (deployment surface) | .github/workflows/ci.yml, .github/dependabot.yml, .github/workflows/release.yml, Makefile |
| M21: Ingest POSTs carrying an `Origin` header refused with 403 (browsers always send Origin on cross-site writes; scripts and agents never do; the endpoint's Content-Type blindness would otherwise let `text/plain` POSTs sail past CORS preflight) | browser-driven dashboard forgery from any visited web page (B1 spoofing) | server.go:123-132; tests internal/ingest/server_test.go; README "Agent feed API" documents it |
| M22: Remote discovery ports parsed as 16-bit with port 0 rejected, so hostile `/proc/net/tcp` output cannot plant impossible forward targets; pinned by FuzzParseDiscoveryOutput | tunnel-set manipulation by a hostile ssh remote (B3 elevation/DoS) | remote/discover.go:93-116; internal/remote/fuzz_test.go:52-109; corpus internal/remote/testdata/fuzz/FuzzParseDiscoveryOutput/ |

Documentation claims checked against code this pass (d9d478d):

- README "Zero vendor libraries", "Engines: how discovery works", and the SSH
  auth chain match the code paths cited above.
- README documents the post-21e3feb scoping accurately: "--bearer TOKEN ...
  sent to --add endpoints only" and the environment-variable table listing
  `GITHUB_TOKEN` for `toktop update`; both match bearer.go and
  selfupdate.go:85-87.
- README documents the Origin-header 403 ("A POST carrying an `Origin`
  header ... is refused with `403`") and the stream-resume semantics added
  since the last review; both match server.go:123-132 and the fail() path
  (:140-153). No code contradicts any README security statement found.
- No SECURITY.md exists, so there are no disclosure-contact or supported-
  version claims to verify; the absence itself is recorded under response
  readiness below.

Single points of failure: the render-time sanitizer (core/sanitize.go) is the
only control standing between all untrusted text and the terminal; every new
UI row must remember to call it. The TOFU callback is the only ssh
authentication-of-host control. The release checksums.txt is the only
integrity anchor for the entire update channel. Each carries several
high-impact threats alone.

## Abuse cases (documented, not demonstrated)

1. **Dashboard poisoning.** A hostile local process (or LAN peer after an
   `--ingest 0.0.0.0` start) POSTs NDJSON naming agent "claude" with huge
   token rates and a plausible note; a browser cannot do this (M21) but any
   script or agent on the host can, without authenticating. Enabling path:
   ingest handlePost -> collector.RecordAgent (collector.go:399-406) -> UI
   agents feed. The operator's view of "which agent is burning tokens" is now
   attacker-chosen; nothing distinguishes forged rows from agentwatch-sourced
   ones.
2. **Bearer token capture via operator-named endpoint** (downgraded from the
   previous "capture via any scanned port", which M1 closed): whatever
   listens on an origin the operator `--add`ed receives
   `Authorization: Bearer <token>` on identification, poll, and probe
   requests (bearer.go:54-60; main.go:166). A typo'd or stale `--add` URL
   pointing at attacker-controlled space discloses the gateway key. Scanned
   ports and ssh-forwarded engines no longer receive the header.
3. **First-connect MITM.** An attacker positioned between operator and target
   before the first ssh connect is pinned as the genuine host; subsequent
   sessions expose vitals polling and port forwarding to them. Enabling path:
   knownhosts.go:50-51 stores first-presented key without out-of-band
   verification.
4. **Malicious-repo install.** `toktop update --repo attacker/toktop` fetches
   that repo's latest release, whose checksums.txt vouches for the attacker's
   binary; after install, hot-reload executes it (update.go:80,
   selfupdate.go:133-195, exec_unix.go:15). Operator-invoked, but the only
   gate between the flag and code execution is the repo's own word.
5. **Client-side-trust inversion (noted for completeness):** the ingest feed
   trusts the sender entirely; there is no server-side notion of an authorized
   harness. Anything claiming to be an agent is rendered as one.

## Gaps needing sec-review attention (ranked)

Recorded as threats with locations; fixes do not happen in this document:

1. **No origin authentication on ingest** (risk 1, Medium): server.go:41-59.
   The routable-bind case is at least announced now (main.go:275-277) and the
   browser sender class is refused (M21); every non-browser local process
   still forges rows unchallenged, and a hostile script can simply omit
   `Origin`.
2. **TOFU first-contact acceptance** (risk 2, Medium): inherent design
   tradeoff, documented in README; candidate improvements (verification
   prompt, SSHFP/known_hosts import) belong to sec-review.
3. **Unsigned release artifacts** (risk 3, Medium-Low): checksums.txt is
   self-published within the same release; no Sigstore/GPG signature ties
   binaries to a key independent of the release pipeline
   (.github/workflows/release.yml, selfupdate.go:133-163).
4. **No per-peer ingest quotas** (Low): bounded per connection
   (server.go:79-120) but not per peer; matters only once gap 1's exposure
   question is settled.
5. **Hot-reload and PATH-based tool execution** (risk 4, Low): acceptable for
   a same-user dev tool; revisit if toktop ever runs privileged.

## Response readiness (notes only)

- **Audit trail:** none. The ingest handler logs nothing, credentials are
  never logged (verified: bearer.Token has one caller chain through
  bearer.Apply), and no state survives process exit, so post-incident
  reconstruction of a poisoning or capture event is impossible from the
  process itself. o11y-review owns log structure; noted here because risk 5
  depends on it.
- **Reported-vulnerability-to-fix path:** undocumented. No SECURITY.md exists
  (no disclosure contact, no supported-version list), and CONTRIBUTING.md
  covers CI gates only. Creating that process is an organizational decision,
  not something this document invents.

## Maintenance rules for this file

- Every entry point, boundary, and mitigation line keeps its file reference so
  the next pass can diff claims against code mechanically.
- New entry point in code => add it here in the same change.
- Fixed vulnerabilities move from "Gaps" into the mitigations table with the
  commit that closed them.
