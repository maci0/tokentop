# tokentop threat model

Living document for the security owner: the whole attack surface on one screen,
with file references so each claim can be re-verified against code. Individual
vulnerabilities and their fixes belong to sec-review; this file records where
they live and what already stands in their way.

- **Last reviewed:** 2026-08-25 (commit 211b24d)
- **Owner:** none assigned in this repository
- **Review cadence:** none scheduled organizationally; re-run whenever an entry
  point, auth path, or bind default changes

Scope: the `tokentop` CLI (single static Go binary) and its deployment
artifacts (GitHub Actions workflows, Makefile release targets). Out of scope:
the vendored-path dependency `github.com/maci0/gauntlet-go` (deps-review owns
it; agentwatch consumes it at internal/agentwatch/agentwatch.go:78).

## Risk-ranked summary

| # | Risk | Boundary | Where | State |
|---|------|----------|-------|-------|
| 1 | Bearer token sent to every probed/attached endpoint without scoping: any local listener on a scanned port (or any `--add` host) receives `Authorization: Bearer <token>` | engine polling | internal/provider/discover.go:72,289, internal/provider/provider.go:57,79, cmd/tokentop/main.go:123-130 | No mitigation |
| 2 | Ingest endpoint accepts unauthenticated events from any local process (any network peer if bound non-loopback via `--ingest`); forged telemetry renders as real agents | local processes -> ingest, network -> ingest | internal/ingest/server.go:41-59,112, cmd/tokentop/main.go:46,199 | No authentication; DoS and injection mitigated (see M3-M5) |
| 3 | Trust-on-first-use accepts a first-contact MITM by design; only key *changes* are refused | operator -> ssh target | internal/remote/knownhosts.go:36-52 | Documented residual risk (README "Auth" section) |
| 4 | Hot-reload re-execs whatever binary occupies the exe path when its identity changes; PATH-based vendor CLI lookup executes tools from `$PATH` | build -> runtime, host -> process | internal/selfreload/exec_unix.go:10, internal/gpu/gpu.go:42-51 | Gated by filesystem permissions / same-user requirement; `--no-hot-reload` exists |
| 5 | No audit trail: ingest requests are not logged, nothing is persisted, so a poisoning or token-capture incident cannot be reconstructed after the fact | response readiness | internal/ingest/server.go (no logging anywhere in handler) | Gap (note only here; o11y-review owns log structure) |

Nothing in this table is demonstrated by attacking anything; every claim cites
code read at review time.

## Assets

What is worth stealing, corrupting, or denying:

- **Engine credentials**: one process-wide bearer token (`internal/bearer/bearer.go:13`),
  sourced from `--bearer`, `OMNIROUTE_API_KEY`, or `TOKENTOP_BEARER`
  (cmd/tokentop/main.go:83-90). Grants API access to gateways like OmniRoute.
- **SSH credentials**: private keys read from `--ssh-key`, `~/.ssh/config`
  IdentityFile, or `~/.ssh/id_*` defaults (internal/remote/auth.go:18-44), the
  ssh-agent socket (auth.go:161-167), and the password from
  `TOKENTOP_SSH_PASSWORD` or the terminal prompt (auth.go:89-114).
- **Host-key pin store**: `os.UserConfigDir()/tokentop/known_hosts`
  (internal/remote/knownhosts.go:16-22). Corrupting it enables a forced
  re-TOFU.
- **Dashboard integrity**: what the operator sees drives triage decisions.
  Poisoned rows are the main prize of the ingest endpoint.
- **Operator terminal integrity**: tokentop renders attacker-shaped text
  (engine model names, remote vitals, event fields) into a TTY; escape-sequence
  injection would hijack clipboard/cursor/title.
- **Monitoring availability**: the dashboard session itself (low value, bounded
  blast radius).
- **Information disclosure via display**: remote host CPU/OS/kernel/GPU
  inventory, engine/model lists, and agent working directories (last two path
  components in event notes, internal/agentwatch/agentwatch.go:188-199) appear
  on screen and in scrollback; screen shares and captures leak them.

## Entry points

Every externally reachable input, with its code location:

1. **Ingest HTTP server** (on by default): `POST /v1/events` (single JSON or
   NDJSON stream), `GET /v1/events`, `GET /healthz`
   (internal/ingest/server.go:48-53). Binds `127.0.0.1:8420` unless `--ingest`
   says otherwise (cmd/tokentop/main.go:46); any address is accepted, including
   routable interfaces. Runs in demo mode too (main.go:109-119).
2. **CLI arguments**: flags including `--bearer` (secret),
   `--ssh-key`, `--add URL` (repeatable), `--ingest ADDR`; positional
   `ssh://[user@]host[:port]` targets (cmd/tokentop/main.go:41-101).
3. **Environment variables**: secrets `OMNIROUTE_API_KEY`,
   `TOKENTOP_BEARER`, `TOKENTOP_SSH_PASSWORD`; plus `SSH_AUTH_SOCK`,
   `TOKENTOP_COLUMNS/LINES` (cmd/tokentop/main.go:83-90,338-343;
   internal/remote/auth.go:99,161).
4. **SSH client sessions** (outbound): shell scripts executed on the remote
   for discovery and vitals (internal/remote/discover.go:87,117-129,136-144;
   internal/remote/stats.go:38-64); all returned text is parsed locally. Local
   relay listeners bound to `127.0.0.1:0` per forwarded port
   (internal/remote/client.go:298).
5. **Engine HTTP polling** (outbound GET/POST): startup probe of ~18 well-known
   localhost ports (internal/provider/discover.go:22-41), process-derived
   candidates (discover.go:91-103), operator-supplied `--add` URLs, and remote
   ports discovered over ssh (cmd/tokentop/main.go:407). Probes POST small
   generations to engines (internal/probe/probe.go).
6. **Agent transcript reading** (local disk): `/proc` walk finds coding-agent
   processes; their transcript files are read every second via the
   gauntlet-go dependency (internal/agentwatch/agentwatch.go:74-96). Agent
   definitions, including transcript paths, load at runtime from
   `~/.gauntlet/agents.json` (agentwatch.go:78).
7. **Config files read at startup**: `~/.ssh/config` (HostName/User/Port/
   IdentityFile override target fields, internal/remote/target.go:104-155) and
   the known_hosts store (knownhosts.go:55-74).
8. **Self hot-reload**: polls the running executable's stat identity and
   re-execs it via `syscall.Exec` with the original argv/environ when changed
   (internal/selfreload/selfreload.go:20-43, exec_unix.go:10;
   cmd/tokentop/main.go:276).
9. **Local system introspection**: `/proc` and sysctl reads
   (internal/procs/, internal/sysmon/), vendor CLIs executed from `$PATH`:
   nvidia-smi, rocm-smi, xpu-smi (internal/gpu/gpu.go:71-107), system_profiler
   (gpu_darwin.go:53), ps (procs_darwin.go:23), a PowerShell CIM query
   (procs_windows.go:32).

Deployment surface: GitHub Actions CI runs gofmt/vet/govulncheck/race tests on
pushes and PRs (.github/workflows/ci.yml, actions pinned by SHA); Dependabot
updates modules and actions (.github/dependabot.yml); tag pushes build release
binaries for five platforms plus a CycloneDX SBOM (.github/workflows/release.yml,
Makefile `release`/`sbom`). Release artifacts ship SHA-256 checksums only; no
signature step exists.

## Trust boundaries and data flow

- **B1: local processes -> ingest server.** Any process running as any local
  user who can reach the socket can POST events. There is no named
  authentication or validation-of-origin point; sanitization happens
  (server.go:160-177) but attribution does not. When `--ingest` binds a
  non-loopback address this boundary widens to the network with no additional
  check at bind time.
- **B2: engine HTTP responses -> tokentop.** Whatever answers on a scanned
  port, a `--add` URL, or a forwarded remote port is treated as an engine:
  its JSON, Prometheus text, version strings, and error bodies flow into
  parsing and rendering. Validation points: body caps (provider.go:66,88),
  NaN/Inf rejection (provider.go:194), render-time sanitization
  (internal/ui/ui.go:683-690,778,851-862).
- **B3: remote ssh host -> tokentop.** A compromised or hostile ssh target
  controls every byte returned by discovery and vitals scripts: `/proc/net/tcp`
  text, cmdline dumps, loadavg/meminfo/CPU/OS/kernel strings, nvidia-smi CSV or
  rocm-smi JSON (stats.go:38-64, discover.go:87-144). Authentication is the ssh
  handshake itself; after that the data crosses unvalidated except by parsers
  (numbers parse-or-zero, e.g. stats.go:159-165, gpu.go:180-189) and the
  renderer's sanitizer.
- **B4: secrets -> code.** Bearer token: enters via argv or env
  (main.go:83-90), lives in a package var (bearer.go:13), leaves in the
  `Authorization` header of *every* engine request - discovery scans, polls,
  and probes alike (discover.go:72, provider.go:57,79, probe.go:204). It is
  never written to disk or logs. SSH password: env or TTY prompt
  (auth.go:89-114), held in memory, used only for password and
  keyboard-interactive mechanisms. Keys: read from disk or agent, sign locally.
  Rotation points: none; all credentials are static for the life of the run.
- **B5: build -> runtime.** Hot-reload trusts that whoever can change the exe
  file is authorized to run code as this user (selfreload.go:27-35 fires on
  size/mtime/dev/ino change). Default-on in interactive runs
  (main.go:234); `--no-hot-reload` disables.
- **B6: user config -> runtime.** `~/.ssh/config` steers where connections go
  and which identity is offered (target.go:104-155); `~/.gauntlet/agents.json`
  defines which processes count as agents and which files are read
  (agentwatch.go:76-78); `PATH` decides which vendor CLIs execute
  (gpu.go:42-51). All three are same-user-writable inputs treated as trusted.

Privilege transitions: tokentop gains no privileges at runtime (no setuid,
no sudo); the ssh connection is the one place code acts with authority beyond
the local process - it executes shell commands on the remote under the target
account (client.go:233-264).

## Threats per boundary (STRIDE, concrete)

**B1 (and widened B1 via `--ingest`):**
- *Spoofing*: forge agent rows ("claude", "codex") with arbitrary throughput,
  models, and notes by POSTing to /v1/events; no origin auth exists
  (server.go:112-190). Renders beside genuine agentwatch data
  (internal/ui/ui.go:851-862).
- *Repudiation*: source address is never recorded, so even loopback forgery
  cannot be attributed afterward.
- *Information disclosure*: none beyond presence (`/healthz` answers any
  requester, server.go:50-53).
- *Denial of service*: mitigated (see the ingest body-cap, deadline, and
  clamp rows in the mitigations map). Residual: no per-peer
  connection quota; each open POST holds an fd plus goroutine until the byte
  cap, body-idle timeout (1m), or absolute lifetime (10m) reaps it
  (server.go:88-91) - a local flood of stalled POSTs is bounded but nonzero.
- *Elevation of privilege*: none; event content reaches only parsing and
  rendering.

**B2:**
- *Spoofing/tampering*: a hostile listener on a scanned port can pose as an
  engine and feed fake metrics, versions, and model lists (display-only
  effect). Fingerprinting order in Identify (discover.go:175-229) decides the
  label; mislabeling is possible, impact is a wrong row.
- *Information disclosure*: **the bearer token rides every identification
  request** (scanGet, discover.go:67-82), so posing as an engine captures
  credential #1. This is the exploitation path behind summary risk 1.
- *Denial of service*: slow responses bounded by scanTimeout 700ms /
  PollTimeout 1.5s / probe timeout 30s; bodies capped (mitigations map:
  response caps row).
- *Elevation*: none; response bytes never reach execution or unsanitized
  output.

**B3:**
- *Spoofing*: TOFU pins nothing on first contact; a MITM present before the
  first connect is pinned as genuine instead (knownhosts.go:50-51). Key change
  refusal is loud and dual-fingerprinted (knownhosts.go:43-49).
- *Tampering/information disclosure*: hostile remote shapes vitals, GPU names,
  and engine lists shown locally; sanitized at render (ui.go:556-599), numbers
  parse defensively - residual risk is misleading content, not injection.
- *Denial of service*: remote can stall each poll up to runTimeout 15s
  (client.go:25) and the handshake up to bannerTimeout 15s
  (client.go:82-109); keepalive turns silent death into a closed connection
  within ~45s (client.go:188-209).
- *Elevation*: remote input never becomes local command text; remote-side
  scripts interpolate only locally generated integers (probeScript,
  discover.go:117-129), so no injection path into the remote shell either.

**B4 (secrets):**
- *Information disclosure*: bearer token exposed to any probed endpoint
  (risk 1); token passed via `--bearer` is visible in process listings -
  README documents the env fallback for exactly this reason (README "Flags"/
  environment section); `TOKENTOP_SSH_PASSWORD` in the environment is readable
  by same-user processes and inherited by children (auth.go:99).
- *Tampering*: known_hosts store is plain text in the user config dir, mode
  0600 in a 0700 dir (knownhosts.go:76-85); a same-user writer can reset pins.

**B5/B6 (build/runtime, configs):**
- *Elevation of privilege*: swapping the exe file converts hot-reload into
  arbitrary code execution as the running user (exec_unix.go:10); substituting
  `nvidia-smi` et al. via `PATH` runs attacker code with user privileges
  (gpu.go:56). Both require write access the user already controls; they matter
  when tokentop runs with more authority than the attacker has (none today -
  noted for future privilege changes).

## Existing mitigations map

Controls verified in code, with the threats they cover:

| Control | Covers | Location |
|---|---|---|
| Terminal escape/control-char sanitizer applied both at ingest and at render time | OSC/CSI clipboard-cursor-title injection from engines, remotes, and events (asset: terminal integrity) | internal/core/sanitize.go:18-98; ingest side server.go:160-177; render side ui.go:217,556-599,683-690,778,851-862,939-944; internal/ui/format.go:124 |
| Ingest body cap 1 MiB + MaxBytesReader | unbounded upload into decode loop (B1 DoS) | server.go:78,116 |
| Ingest read deadlines: 10 min absolute lifetime, 1 min idle extension, 5 s header timeout, 2 min idle reap | slowloris/drip DoS (B1) | server.go:39,54-58,88-116 |
| Event field clamps (agent 64, model 128, note 512, kind 24 runes) + retention caps on stored feeds | memory pinning via oversized or numerous events (B1 DoS) | server.go:160-181,234-242; collector.go:395,410 |
| Negative token counts clamped to zero; unknown kinds defaulted | junk values entering retained state (B1 tampering) | server.go:168-181 |
| Engine response caps: 4 MiB JSON, 8 MiB text, 256-rune error snippets | memory blowup and log flooding from hostile engines (B2 DoS/disclosure) | provider/provider.go:66,88; httperr/httperr.go:17-31 |
| NaN/Inf rejection in metrics and vendor CSV/JSON coercion | poisoned counters/rates propagating through EMA history (B2 tampering) | provider.go:194; gpu.go:180-189; collector.go:310-317 |
| Probe generation ceiling 512 tokens; fixed small prompt | probes becoming compute-amplification attacks against engines (B2 DoS) | probe.go:24-29,31 |
| Poll/scan/probe timeouts (700ms/1.5s/30s) + context-bounded requests | hung-engine DoS (B2/B3) | discover.go:20; provider.go:26; probe.go:20 |
| TOFU host-key store with loud change refusal, 0600 file in 0700 dir | silent MITM after first contact (B3 spoofing) | knownhosts.go:27-53,76-85 |
| Banner deadline lifted only on complete version line; 15s command timeout; keepalive with bounded probe waits | trickle/silent-peer hangs (B3 DoS) | client.go:25,82-109,188-209 |
| Local-only defaults: forward listeners on 127.0.0.1, ingest on 127.0.0.1:8420 | accidental network exposure (B1 widening) | client.go:298; main.go:46 |
| Remote shell scripts: static bodies, only locally generated integers interpolated; no secret material sent to remote scripts | command injection into remote shell (B3 elevation) | discover.go:87,117-144; stats.go:38-64 |
| Password prompt gated on TTY; encrypted keys skipped with guidance | credential handling in headless runs (B4) | auth.go:33-44,103-106 |
| Flag validation exits 2; unknown `TOKENTOP_*` env warned | misconfiguration acting as silent security-relevant behavior change | main.go:66-70,323-334,345-361 |
| Supply chain: govulncheck in CI, Dependabot, SHA-pinned workflow actions, SBOM in releases | vulnerable-dependency drift (deployment surface) | .github/workflows/ci.yml, .github/dependabot.yml, .github/workflows/release.yml, Makefile |

Documentation claims checked against code this pass:

- README "Zero vendor libraries", "Engines: how discovery works", and the SSH
  auth chain match the code paths cited above.
- README's advice to prefer env vars over `--bearer` matches reality
  (main.go:83-90); no code contradicts any README security statement found.
- No SECURITY.md exists, so there are no disclosure-contact or supported-
  version claims to verify; the absence itself is recorded under response
  readiness below.

Single points of failure: the render-time sanitizer (core/sanitize.go) is the
only control standing between all untrusted text and the terminal; every new
UI row must remember to call it. The TOFU callback is the only ssh
authentication-of-host control. Both carry several high-impact threats each.

## Abuse cases (documented, not demonstrated)

1. **Dashboard poisoning.** A hostile local process (or LAN peer after an
   `--ingest 0.0.0.0` start) POSTs NDJSON naming agent "claude" with huge
   token rates and a plausible note. Enabling path: ingest handlePost ->
   collector.RecordAgent (collector.go:391-398) -> UI agents feed. The
   operator's view of "which agent is burning tokens" is now attacker-chosen;
   nothing distinguishes forged rows from agentwatch-sourced ones.
2. **Bearer token capture.** Any user on the host starts a trivial HTTP
   listener on a well-known port (8080, 8000, ...). Startup discovery sends
   `GET /`-family requests with `Authorization: Bearer <token>` attached
   (discover.go:72). The listener logs the gateway API key. Same result with
   zero timing constraints via `--add http://attacker-controlled/`.
3. **First-connect MITM.** An attacker positioned between operator and target
   before the first ssh connect is pinned as the genuine host; subsequent
   sessions expose vitals polling and port forwarding to them. Enabling path:
   knownhosts.go:50-51 stores first-presented key without out-of-band
   verification.
4. **Client-side-trust inversion (noted for completeness):** the ingest feed
   trusts the sender entirely; there is no server-side notion of an authorized
   harness. Anything claiming to be an agent is rendered as one.

## Gaps needing sec-review attention (ranked)

Recorded as threats with locations; fixes do not happen in this document:

1. **Unscoped bearer broadcast** (risk 1, High): token applied to every
   discovery/poll/probe request regardless of destination trust
   (discover.go:72, provider.go:57,79,204, main.go:123-130).
2. **No origin authentication on ingest** (risk 2, Medium): server.go:41-59;
   also no warning when `--ingest` binds a routable address (main.go:200-218).
3. **TOFU first-contact acceptance** (risk 3, Medium): inherent design tradeoff,
   documented in README; candidate improvements (verification prompt, SSHFP/
   known_hosts import) belong to sec-review.
4. **No per-peer ingest quotas** (Low): bounded per connection (server.go:78-91)
   but not per peer; matters only once risk 2's exposure question is settled.
5. **Hot-reload and PATH-based tool execution** (risk 4, Low): acceptable for a
   same-user dev tool; revisit if tokentop ever runs privileged.

## Response readiness (notes only)

- **Audit trail:** none. The ingest handler logs nothing, credentials are
  never logged (verified: bearer.Token has one caller, bearer.Apply), and no
  state survives process exit, so post-incident reconstruction of a poisoning
  or capture event is impossible from the process itself. o11y-review owns log
  structure; noted here because risk 5 depends on it.
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
