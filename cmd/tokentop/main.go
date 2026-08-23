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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

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
		showVer   = flag.Bool("version", false, "print version")
	)
	flag.Var(&adds, "add", "attach an openai-compatible backend URL (repeatable)")
	flag.Parse()

	if *showVer {
		fmt.Println("tokentop", version)
		return
	}
	log.SetFlags(0)

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

		if *probeSecs > 0 {
			d := time.Duration(*probeSecs) * time.Second
			go func() {
				t := time.NewTicker(d)
				defer t.Stop()
				col.ProbeAll()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						col.ProbeAll()
					}
				}
			}()
		}
	}

	if !*noIngest && recorder != nil {
		srv, err := ingest.New(*ingestArg, recorder)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tokentop: ingest disabled (%v)\n", err)
		} else {
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
		IngestAddr: *ingestArg,
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
	self, _ := os.Executable()
	var (
		mu       sync.Mutex
		current  *tea.Program
		reloaded atomic.Bool
	)
	if hotReload {
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

	for {
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
			return
		}
		return // clean quit
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

// attachRemote discovers engines on an ssh target and starts a tunnel plus
// remote stats sampling. The tunnel lives until ctx is cancelled.
func attachRemote(ctx context.Context, tgt remote.Target) ([]provider.Provider, *remote.Stats, error) {
	ports, err := remote.DiscoverPorts(ctx, tgt, provider.CandidatePorts())
	if err != nil {
		return nil, nil, err
	}
	if len(ports) == 0 {
		return nil, nil, fmt.Errorf("no inference ports listening on %s", tgt.Host)
	}
	tun, err := remote.StartTunnel(ctx, tgt, ports)
	if err != nil {
		return nil, nil, err
	}
	go func() {
		<-ctx.Done()
		tun.Close()
	}()

	var providers []provider.Provider
	for rport, lport := range tun.Local {
		base := fmt.Sprintf("http://127.0.0.1:%d", lport)
		label := fmt.Sprintf("%s:%d", tgt.Host, rport)
		if kind := provider.Identify(ctx, base); kind != "" {
			providers = append(providers, provider.NewOpenAICompat(base, label, kind))
		}
	}
	stats := &remote.Stats{Target: tgt}
	go stats.Run(ctx, 5*time.Second)
	return providers, stats, nil
}
