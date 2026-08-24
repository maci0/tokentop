// tokentop: btop-style dashboard for LLM inference engines and the agents
// hammering them.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"tokentop/internal/bearer"
	"tokentop/internal/collector"
	"tokentop/internal/core"
	"tokentop/internal/demo"
	"tokentop/internal/ingest"
	"tokentop/internal/provider"
	"tokentop/internal/remote"
	"tokentop/internal/selfreload"
	"tokentop/internal/sysmon"
	"tokentop/internal/ui"
)

const version = "0.1.0"

func main() {
	var (
		demoMode  = flag.Bool("demo", false, "run against a simulated fleet instead of real backends")
		adds      flagAddList
		probeSecs = flag.Int("probe", 0, "auto-probe every N seconds (0=off)")
		interval  = flag.Duration("interval", time.Second, "poll interval")
		ingestArg = flag.String("ingest", "127.0.0.1:8420", "agent event ingest listen address")
		noIngest  = flag.Bool("no-ingest", false, "disable the agent event HTTP endpoint")
		once      = flag.Bool("once", false, "render one frame and exit (non-interactive)")
		frames    = flag.Int("frames", 2, "with --once: snapshots to accumulate before rendering")
		noReload  = flag.Bool("no-hot-reload", false, "disable restart-on-rebuild (dev convenience)")
		seed      = flag.Int64("seed", 42, "demo RNG seed")
		sshKey    = flag.String("ssh-key", "", "private key for ssh:// targets (overrides ~/.ssh/config)")
		bearerArg = flag.String("bearer", "", "bearer token sent to engines (OmniRoute etc.)")
		showVer   = flag.Bool("version", false, "print version")
	)
	flag.Var(&adds, "add", "attach an openai-compatible backend URL (repeatable)")
	flag.Parse()

	if *showVer {
		fmt.Println("tokentop", version)
		return
	}
	log.SetFlags(0)

	// Bearer token for gateways that require API keys (OmniRoute et al).
	// Flag wins; OMNIROUTE_API_KEY / TOKENTOP_BEARER are convenience fallbacks.
	switch {
	case *bearerArg != "":
		bearer.Set(*bearerArg)
	case os.Getenv("OMNIROUTE_API_KEY") != "":
		bearer.Set(os.Getenv("OMNIROUTE_API_KEY"))
	case os.Getenv("TOKENTOP_BEARER") != "":
		bearer.Set(os.Getenv("TOKENTOP_BEARER"))
	}

	// positional ssh:// targets
	var remoteTargets []string
	for _, arg := range flag.Args() {
		if strings.HasPrefix(arg, "ssh://") {
			remoteTargets = append(remoteTargets, arg)
		} else {
			fmt.Fprintf(os.Stderr, "tokentop: unexpected argument %q (did you mean --add %s?)\n", arg, arg)
			os.Exit(2)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ch := make(chan core.Snapshot, 8)
	var prober ui.Prober

	// The agent event endpoint runs in every mode so harnesses can always
	// feed the dashboard.
	var recorder ingest.Recorder
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
			if p := provider.Attach(ctx, strings.TrimRight(raw, "/")); p != nil {
				providers = append(providers, p)
			} else {
				fmt.Fprintf(os.Stderr, "tokentop: nothing recognized at %s; polling as generic openai anyway\n", raw)
				providers = append(providers, provider.NewOpenAICompat(raw, raw, core.KindOpenAI))
			}
		}

		var sysWrap func() core.SysSample
		for _, raw := range remoteTargets {
			tgt, err := remote.ParseTarget(raw)
			if err != nil {
				fmt.Fprintln(os.Stderr, "tokentop:", err)
				os.Exit(2)
			}
			// Only when set: an empty flag must keep the IdentityFile
			// resolved from ~/.ssh/config by ParseTarget.
			if *sshKey != "" {
				tgt.KeyFile = *sshKey
			}
			rp, rsys, rerr := attachRemote(ctx, tgt)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "tokentop: %v\n", rerr)
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
			fmt.Fprintf(os.Stderr, "tokentop: attached %d engine(s) via ssh on %s\n",
				len(rp), tgt.Host)
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

	if !*noIngest && recorder != nil {
		srv, err := ingest.New(*ingestArg, recorder)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tokentop: ingest disabled (%v)\n", err)
		} else {
			feedAddr = srv.Addr()
			go func() {
				if err := srv.Serve(); err != nil {
					fmt.Fprintf(os.Stderr, "tokentop: ingest stopped: %v\n", err)
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
	}

	if *once {
		runOnce(cfg, ch, *frames)
		return
	}

	runTUI(ctx, cfg, ch, !*noReload)
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
			fmt.Fprintf(os.Stderr, "tokentop: hot reload disabled (%v)\n", selfErr)
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
		fmt.Fprintln(os.Stderr, "tokentop:", err)
		os.Exit(1)
	}
	if reloaded.Load() {
		fmt.Fprintln(os.Stderr, "tokentop: binary changed, restarting…")
		if err := selfreload.ReExec(self, os.Args, os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, "tokentop:", err)
		}
	}
}

// runOnce prints a single rendered frame sized to the terminal (or 120x38).
// TOKENTOP_COLUMNS / TOKENTOP_LINES override detection (useful for capture).
func runOnce(cfg ui.Config, ch <-chan core.Snapshot, n int) {
	w, h := 120, 38
	if tw, th, err := term.GetSize(int(os.Stdout.Fd())); err == nil && tw > 40 && th > 20 {
		w, h = tw, th
	}
	if v, err := strconv.Atoi(os.Getenv("TOKENTOP_COLUMNS")); err == nil && v > 40 {
		w = v
	}
	if v, err := strconv.Atoi(os.Getenv("TOKENTOP_LINES")); err == nil && v > 20 {
		h = v
	}
	if n < 1 {
		n = 1
	}
	var snap core.Snapshot
	for i := 0; i < n; i++ { // several ticks so charts carry some history
		select {
		case snap = <-ch:
		case <-time.After(5 * time.Second):
			fmt.Fprintln(os.Stderr, "tokentop: timed out waiting for telemetry")
			os.Exit(1)
		}
	}
	fmt.Println(ui.StaticFrame(cfg, snap, w, h))
}

type flagAddList []string

func (a *flagAddList) String() string     { return strings.Join(*a, ",") }
func (a *flagAddList) Set(v string) error { *a = append(*a, v); return nil }

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
			fmt.Fprintf(os.Stderr, "tokentop: ssh connection to %s lost (%v)\n", tgt.Host, cli.Err())
		}
	}()

	rports := sortedKeys(fwd) // deterministic order
	bases := make([]string, len(rports))
	for i, rport := range rports {
		bases[i] = fmt.Sprintf("http://127.0.0.1:%d", fwd[rport])
	}
	// Identify concurrently: each probe chains several requests with
	// per-request timeouts, and a remote host can forward many candidates.
	kinds := make([]string, len(bases))
	var wg sync.WaitGroup
	for i, base := range bases {
		wg.Add(1)
		go func(i int, base string) {
			defer wg.Done()
			kinds[i] = provider.Identify(ctx, base)
		}(i, base)
	}
	wg.Wait()

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
		fmt.Fprintf(os.Stderr, "tokentop: %s:%d is listening but speaks no recognized engine API; skipping\n",
			tgt.Host, p)
	}
	stats := &remote.Stats{Client: cli}
	go stats.Run(ctx, 5*time.Second)
	return providers, stats, nil
}

// sortedKeys returns the tunnel's remote ports in ascending order so backend
// ordering does not depend on Go map iteration.
func sortedKeys(m map[int]int) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
