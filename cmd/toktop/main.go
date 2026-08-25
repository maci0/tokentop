// toktop: btop-style dashboard for LLM inference engines and the agents
// hammering them.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/maci0/toktop/agentusage"
	"github.com/maci0/toktop/internal/agentwatch"
	"github.com/maci0/toktop/internal/bearer"
	"github.com/maci0/toktop/internal/collector"
	"github.com/maci0/toktop/internal/core"
	"github.com/maci0/toktop/internal/demo"
	"github.com/maci0/toktop/internal/ingest"
	"github.com/maci0/toktop/internal/provider"
	"github.com/maci0/toktop/internal/remote"
	"github.com/maci0/toktop/internal/selfreload"
	"github.com/maci0/toktop/internal/sysmon"
	"github.com/maci0/toktop/internal/ui"
)

// var, not const: release builds stamp it via -ldflags "-X main.version=...".
var version = "0.1.0"

func main() {
	// One subcommand, taken before flag parsing: everything else about this
	// CLI is flags and ssh:// targets.
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(runUpdate(context.Background(), os.Stdout, os.Args[2:]))
	}
	var (
		demoMode  = flag.Bool("demo", false, "run against a simulated fleet instead of real backends")
		adds      flagAddList
		probeSecs = flag.Int("probe", 0, "auto-probe every N seconds (0=off)")
		interval  = flag.Duration("interval", time.Second, "poll interval")
		ingestArg = flag.String("ingest", "127.0.0.1:8420", "agent event ingest listen address")
		noIngest  = flag.Bool("no-ingest", false, "disable the agent event HTTP endpoint")
		agents    = flag.Bool("agents", false, "watch AI coding agents on this machine by reading their session transcripts")
		opencode  = flag.Bool("opencode-db", false, "with --agents: also read opencode's SQLite session database (needs a build with -tags sqlite)")
		once      = flag.Bool("once", false, "render one frame and exit (non-interactive)")
		frames    = flag.Int("frames", 2, "with --once: snapshots to accumulate before rendering")
		noReload  = flag.Bool("no-hot-reload", false, "disable restart-on-rebuild (dev convenience)")
		seed      = flag.Int64("seed", 42, "demo RNG seed")
		sshKey    = flag.String("ssh-key", "", "private key for ssh:// targets (overrides ~/.ssh/config)")
		bearerArg = flag.String("bearer", "", "bearer token sent to engines (OmniRoute etc.)")
		showVer   = flag.Bool("version", false, "print version")
		showHelp  bool
	)
	flag.BoolVar(&showHelp, "help", false, "show help and exit")
	flag.BoolVar(&showHelp, "h", false, "shorthand for --help")
	flag.Var(&adds, "add", "attach an openai-compatible backend URL (repeatable)")
	// Error paths (unknown flag, bad value) print this usage on stderr and
	// exit 2; -h/--help is handled below so it lands on stdout with exit 0.
	flag.Usage = func() { usage(os.Stderr) }
	flag.Parse()

	if showHelp {
		usage(os.Stdout)
		return
	}
	if *showVer {
		fmt.Println("toktop", version)
		return
	}
	log.SetFlags(0)

	if err := validateFlags(*once, *interval, *probeSecs, *frames); err != nil {
		fmt.Fprintf(os.Stderr, "toktop: %v\n", err)
		os.Exit(2)
	}
	if *once {
		if err := validateOnceEnv(); err != nil {
			fmt.Fprintf(os.Stderr, "toktop: %v\n", err)
			os.Exit(2)
		}
	}
	warnUnknownEnv()

	// Flags the user passed explicitly, for warnings about no-op combos.
	explicit := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	warnIgnoredFlags(explicit, *demoMode, *once, *agents)

	// Only when --ingest was given explicitly should an unusable listen
	// address abort the run; the default-enabled endpoint degrades gracefully.
	ingestSet := explicit["ingest"]

	// Bearer token for gateways that require API keys (OmniRoute et al).
	// Flag wins; OMNIROUTE_API_KEY / TOKTOP_BEARER are convenience fallbacks.
	switch {
	case *bearerArg != "":
		bearer.Set(*bearerArg)
	case os.Getenv("OMNIROUTE_API_KEY") != "":
		bearer.Set(os.Getenv("OMNIROUTE_API_KEY"))
	case os.Getenv("TOKTOP_BEARER") != "":
		bearer.Set(os.Getenv("TOKTOP_BEARER"))
	}

	// positional ssh:// targets
	var remoteTargets []string
	for _, arg := range flag.Args() {
		if strings.HasPrefix(arg, "ssh://") {
			remoteTargets = append(remoteTargets, arg)
		} else {
			fmt.Fprintf(os.Stderr, "toktop: unexpected argument %q (did you mean --add %s?)\n", arg, arg)
			os.Exit(2)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ch := make(chan core.Snapshot, 8)
	var prober ui.Prober
	feedErr := make(chan string, 1) // carries the ingest endpoint's death to the UI

	// The agent event endpoint runs in every mode so harnesses can always
	// feed the dashboard.
	var recorder ingest.Recorder
	// Endpoints toktop is already measuring. An agent generating through one
	// of them has its tokens reported by the engine, which sees every client;
	// counting the agent as well would double the total.
	var engineAddrs agentwatch.Engines
	feedAddr := "" // advertised by the UI only while the endpoint is live

	switch {
	case *demoMode:
		src := demo.NewSource(*interval, *seed)
		go src.Run(ctx, ch)
		prober = src
		recorder = src

	default:
		providers := provider.Discover(ctx)
		for _, raw := range adds {
			// The token rides only to endpoints the operator named: discovery
			// probes every well-known port on spec, and whatever answers there
			// must not be able to harvest the credential.
			bearer.Allow(raw)
			if p := provider.Attach(ctx, strings.TrimRight(raw, "/")); p != nil {
				providers = append(providers, p)
			} else {
				fmt.Fprintf(os.Stderr, "toktop: nothing recognized at %s; polling as generic openai anyway\n", raw)
				providers = append(providers, provider.NewOpenAICompat(raw, raw, core.KindOpenAI))
			}
		}

		var sysWrap func() core.SysSample
		for _, raw := range remoteTargets {
			tgt, err := remote.ParseTarget(raw)
			if err != nil {
				fmt.Fprintln(os.Stderr, "toktop:", err)
				os.Exit(2)
			}
			// Only when set: an empty flag must keep the IdentityFile
			// resolved from ~/.ssh/config by ParseTarget.
			if *sshKey != "" {
				tgt.KeyFile = *sshKey
			}
			rp, rsys, rerr := attachRemote(ctx, tgt)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "toktop: %v\n", rerr)
				continue
			}
			providers = append(providers, rp...)
			prev := sysWrap
			sysWrap = func() core.SysSample {
				var s core.SysSample
				if prev != nil {
					s = prev()
				} else {
					s = sysmon.Sample()
				}
				rsys.Merge(&s)
				return s
			}
			fmt.Fprintf(os.Stderr, "toktop: attached %d engine(s) via ssh on %s\n",
				len(rp), tgt.Host)
		}

		engineAddrs = func() []string {
			out := make([]string, 0, len(providers))
			for _, p := range providers {
				out = append(out, p.Addr())
			}
			return out
		}

		col := collector.New(providers, *interval)
		if sysWrap != nil {
			col.SetSysFn(sysWrap)
		}
		go col.Run(ctx, ch)
		prober = col
		recorder = col
	}

	if *probeSecs > 0 && prober != nil {
		d := time.Duration(*probeSecs) * time.Second
		go func() {
			t := time.NewTicker(d)
			defer t.Stop()
			prober.ProbeAll()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					prober.ProbeAll()
				}
			}
		}()
	}

	// Agents running on this machine, read from the transcripts they already
	// write. This is the counterpart to the HTTP endpoint below: it needs no
	// cooperation from the agent, so a claude or codex started in a terminal
	// shows up without anyone wiring toktop into it.
	//
	// Opt-in, because it means scanning this machine's processes and reading
	// files the operator never pointed at toktop. Watching engines does not
	// imply consent to that.
	if *agents {
		loadAgentDefs()
	}
	if *agents && *opencode && !agentusage.EnableOpenCodeDB(true) {
		// Silence here would look like an agent that generates nothing.
		fmt.Fprintln(os.Stderr, "toktop: --opencode-db needs a build with -tags sqlite; opencode will report no tokens")
	}
	if *agents && recorder != nil {
		// engineAddrs is nil in demo mode, where nothing real is measured.
		go agentwatch.New(recorder, engineAddrs, 0, 0).Run(ctx)
	}

	if !*noIngest && recorder != nil {
		srv, err := ingest.New(*ingestArg, recorder)
		if err != nil {
			if ingestSet {
				// The operator explicitly asked for this endpoint; continuing
				// would run the dashboard without the event feed they asked
				// for, with only a stderr line lost under the alt screen.
				fmt.Fprintf(os.Stderr, "toktop: --ingest %s unusable: %v\n", *ingestArg, err)
				os.Exit(2)
			}
			fmt.Fprintf(os.Stderr, "toktop: ingest disabled (%v)\n", err)
		} else {
			feedAddr = srv.Addr()
			if routableBind(feedAddr) {
				fmt.Fprintf(os.Stderr, "toktop: warning: ingest endpoint %s accepts unauthenticated events from any reachable peer\n", feedAddr)
			}
			go func() {
				if err := srv.Serve(); err != nil {
					fmt.Fprintf(os.Stderr, "toktop: ingest stopped: %v\n", err)
					select { // the alt screen hides stderr; tell the UI too
					case feedErr <- err.Error():
					default:
					}
				}
			}()
			defer srv.Close()
		}
	}

	cfg := ui.Config{
		Version:    version,
		Demo:       *demoMode,
		IngestAddr: feedAddr,
		PollEvery:  *interval,
		Prober:     prober,
		FeedErr:    feedErr,
		Agents:     *agents,
	}

	if *once {
		runOnce(cfg, ch, *frames)
		return
	}

	runTUI(ctx, cfg, ch, !*noReload)
}

// routableBind reports whether a bound listen address is reachable from
// beyond this host: a wildcard or non-loopback interface rather than
// loopback. The ingest endpoint authenticates nothing, so such a bind is
// worth naming at startup instead of leaving the widening silent.
func routableBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false // no port to split: treat as local, not as an alarm
	}
	if host == "" {
		return true // ":port" binds every interface
	}
	ip := net.ParseIP(host)
	return ip != nil && !ip.IsLoopback()
}

// runTUI runs the dashboard, restarting into a fresh binary whenever the
// executable on disk is rebuilt (dev hot-reload).
func runTUI(ctx context.Context, cfg ui.Config, ch <-chan core.Snapshot, hotReload bool) {
	self, selfErr := os.Executable()
	var (
		mu       sync.Mutex
		current  *tea.Program
		reloaded atomic.Bool
	)
	if hotReload {
		if selfErr != nil {
			fmt.Fprintf(os.Stderr, "toktop: hot reload disabled (%v)\n", selfErr)
			hotReload = false
		} else {
			wctx, cancel := context.WithCancel(ctx)
			defer cancel()
			go selfreload.Watch(wctx, self, 400*time.Millisecond, func() {
				reloaded.Store(true)
				mu.Lock()
				p := current
				mu.Unlock()
				if p != nil {
					p.Quit()
				}
			})
		}
	}

	prog := tea.NewProgram(ui.New(cfg, ch), tea.WithAltScreen())
	mu.Lock()
	current = prog
	mu.Unlock()

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "toktop:", err)
		os.Exit(1)
	}
	if reloaded.Load() {
		fmt.Fprintln(os.Stderr, "toktop: binary changed, restarting…")
		selfreload.Restart(self, os.Args, os.Environ())
	}
}

// runOnce prints a single rendered frame sized to the terminal (or 120x38).
// TOKTOP_COLUMNS / TOKTOP_LINES override detection (useful for capture);
// validateOnceEnv rejected unusable values before this runs.
func runOnce(cfg ui.Config, ch <-chan core.Snapshot, n int) {
	w, h := 120, 38
	if tw, th, err := term.GetSize(int(os.Stdout.Fd())); err == nil && tw > 40 && th > 20 {
		w, h = tw, th
	}
	if v, err := strconv.Atoi(os.Getenv("TOKTOP_COLUMNS")); err == nil && v >= minFrameColumns {
		w = v
	}
	if v, err := strconv.Atoi(os.Getenv("TOKTOP_LINES")); err == nil && v >= minFrameLines {
		h = v
	}
	// Snapshots land one poll interval apart, so a slow-polling host needs a
	// proportionally patient wait: a fixed cap would abort a healthy
	// --interval 10s run before its second frame ever arrives.
	wait := 5 * time.Second
	if d := 3 * cfg.PollEvery; d > wait {
		wait = d
	}
	var snap core.Snapshot
	for range n { // several ticks so charts carry some history
		select {
		case snap = <-ch:
		case <-time.After(wait):
			fmt.Fprintln(os.Stderr, "toktop: timed out waiting for telemetry")
			os.Exit(1)
		}
	}
	fmt.Println(ui.StaticFrame(cfg, snap, w, h))
}

type flagAddList []string

func (a *flagAddList) String() string     { return strings.Join(*a, ",") }
func (a *flagAddList) Set(v string) error { *a = append(*a, v); return nil }

// usage prints the full help screen: what toktop is, how to invoke it,
// worked examples, generated flag docs and where the env fallbacks live.
// -h/--help sends it to stdout so piping works (`toktop --help | grep
// probe`); flag-package error paths call it with stderr.
func usage(w io.Writer) {
	out := flag.CommandLine.Output()
	flag.CommandLine.SetOutput(w)
	defer flag.CommandLine.SetOutput(out)
	fmt.Fprint(w, `toktop - btop-style dashboard for LLM inference engines and the agents hammering them

Usage:
  toktop [flags] [ssh://user@host ...]
  toktop update [--check] [--repo owner/name]   install the latest release

Examples:
  toktop --demo                simulated fleet, works instantly
  toktop                       auto-discover engines on this machine
  toktop ssh://maci@box        watch another host's engines over ssh
  toktop --add http://10.0.0.5:8000   attach an endpoint (repeatable)
  toktop --agents              also watch coding agents on this machine
  toktop --agents --opencode-db  ...including opencode's session database
  toktop --once >frame.txt     render one static frame and exit

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprint(w, `
Positional arguments are ssh:// targets and may repeat; anything else is
rejected with a hint. Bearer tokens fall back to $OMNIROUTE_API_KEY then
$TOKTOP_BEARER (--bearer wins); ssh passwords come from the terminal prompt
or $TOKTOP_SSH_PASSWORD. See README.md for all environment variables.
`)
}

// warnIgnoredFlags names flags passed explicitly but with no effect in the
// chosen mode: a silently dropped knob looks like a broken feature.
func warnIgnoredFlags(set map[string]bool, demo, once, agents bool) {
	if set["opencode-db"] && !agents {
		fmt.Fprintln(os.Stderr, "toktop: --opencode-db has no effect without --agents")
	}
	if set["seed"] && !demo {
		fmt.Fprintln(os.Stderr, "toktop: --seed has no effect without --demo")
	}
	if set["frames"] && !once {
		fmt.Fprintln(os.Stderr, "toktop: --frames has no effect without --once")
	}
}

// validateFlags rejects out-of-range values at startup instead of letting
// them be coerced downstream (a non-positive interval silently became 1s
// inside the collector): a running dashboard that ignores what it was asked
// to do is a misconfiguration nobody can see.
func validateFlags(once bool, interval time.Duration, probeSecs, frames int) error {
	if interval <= 0 {
		return fmt.Errorf("--interval must be positive, got %s", interval)
	}
	if probeSecs < 0 {
		return fmt.Errorf("--probe must be >= 0 (0 disables auto-probe), got %d", probeSecs)
	}
	if once && frames < 1 {
		return fmt.Errorf("--frames must be >= 1, got %d", frames)
	}
	return nil
}

// Frame floors: below these the static frame cannot lay out legibly. The
// README documents the overrides as "> 40" / "> 20".
const (
	minFrameColumns = 41
	minFrameLines   = 21
)

// validateOnceEnv rejects a set-but-unusable frame override before --once
// renders: a capture sized by a typo'd variable must fail loudly rather than
// come out at the fallback size with nothing explaining why. Unset or empty
// means default, matching how every other optional setting reads here.
func validateOnceEnv() error {
	for _, e := range [...]struct {
		name  string
		least int
	}{
		{"TOKTOP_COLUMNS", minFrameColumns},
		{"TOKTOP_LINES", minFrameLines},
	} {
		v := os.Getenv(e.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < e.least {
			return fmt.Errorf("$%s must be an integer >= %d, got %q", e.name, e.least, v)
		}
	}
	return nil
}

// toktopEnvVars are the TOKTOP_* environment variables this build reads;
// see also OMNIROUTE_API_KEY, SSH_AUTH_SOCK and the ssh defaults.
var toktopEnvVars = map[string]bool{
	"TOKTOP_BEARER":       true,
	"TOKTOP_SSH_PASSWORD": true,
	"TOKTOP_COLUMNS":      true,
	"TOKTOP_LINES":        true,
}

// warnUnknownEnv reports unrecognized TOKTOP_* variables once at startup:
// a misspelled knob would otherwise be ignored silently and look like a
// no-op feature.
func warnUnknownEnv() {
	var unknown []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "TOKTOP_") || toktopEnvVars[name] {
			continue
		}
		unknown = append(unknown, name)
	}
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "toktop: ignoring unknown environment variable(s): %s\n",
			strings.Join(unknown, ", "))
	}
}

// loadAgentDefs pulls in ~/.gauntlet/agents.json so --agents can follow
// agents toktop was not built to know (in-house wrappers, the pi family),
// including where they keep their transcripts. A missing file is the normal
// case; a malformed one is reported instead of swallowed, because agents
// silently missing from the watch look exactly like agents doing nothing.
func loadAgentDefs() {
	path := agentusage.DefinitionsPath()
	if path == "" {
		return // no home directory resolved; nothing to load
	}
	if err := agentusage.LoadDefinitions(path); err != nil {
		fmt.Fprintf(os.Stderr, "toktop: %s: %v; watching only the built-in agents\n", path, err)
	}
}

// attachRemote connects to an ssh target, discovers engines, relays their
// ports through the connection and starts remote stats sampling. Everything
// shares one in-process ssh client; its death mid-run is reported once.
func attachRemote(ctx context.Context, tgt remote.Target) ([]provider.Provider, *remote.Stats, error) {
	cli, err := remote.Connect(ctx, tgt)
	if err != nil {
		return nil, nil, err
	}

	wellKnown := provider.CandidatePorts()
	disc, err := remote.Discover(ctx, cli, wellKnown)
	if err != nil {
		cli.Close()
		return nil, nil, err
	}
	ports := disc.ForwardSet(wellKnown)
	if len(ports) == 0 {
		cli.Close()
		return nil, nil, fmt.Errorf("no inference ports listening on %s", tgt.Host)
	}
	fwd, err := cli.Forward(ports)
	if err != nil {
		cli.Close()
		return nil, nil, err
	}
	go func() {
		select {
		case <-ctx.Done():
			return // normal shutdown closes the client by design
		case <-cli.Done():
			if ctx.Err() != nil {
				return // shutdown raced the drop; not a loss
			}
			fmt.Fprintf(os.Stderr, "toktop: ssh connection to %s lost (%v)\n", tgt.Host, cli.Err())
		}
	}()

	// Ascending remote ports: backend order must not depend on map iteration.
	rports := slices.Sorted(maps.Keys(fwd))
	bases := make([]string, len(rports))
	for i, rport := range rports {
		bases[i] = fmt.Sprintf("http://127.0.0.1:%d", fwd[rport])
	}
	// Identify concurrently: provider fans the probes out per candidate.
	kinds := provider.IdentifyAll(ctx, bases)

	var providers []provider.Provider
	var skipped []int
	for i, kind := range kinds {
		if kind != "" {
			label := fmt.Sprintf("%s:%d", tgt.Host, rports[i])
			providers = append(providers, provider.NewOpenAICompat(bases[i], label, kind))
			continue
		}
		skipped = append(skipped, rports[i])
	}
	for _, p := range skipped {
		fmt.Fprintf(os.Stderr, "toktop: %s:%d is listening but speaks no recognized engine API; skipping\n",
			tgt.Host, p)
	}
	stats := &remote.Stats{Client: cli}
	go stats.Run(ctx, 5*time.Second)
	return providers, stats, nil
}
