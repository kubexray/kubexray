// Command kubexray is the flight recorder for a Kubernetes cluster.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/cli"
	"github.com/kubexray/kubexray/internal/cluster"
	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/recorder"
	"github.com/kubexray/kubexray/internal/server"
	"github.com/kubexray/kubexray/internal/version"
	"github.com/kubexray/kubexray/internal/web"
)

// flushInterval is how often changes are batched into a delta (ADR-0003).
const flushInterval = 100 * time.Millisecond

// maxHistoryMB bounds --history-max-mb (64 GiB) well below int overflow.
const maxHistoryMB = 64 << 10

const usage = `kubexray is the flight recorder for your Kubernetes cluster.

Usage:
  kubexray                     open the live map in your browser
  kubexray incidents           list what's broken right now, and why
  kubexray why <kind>/<name>   explain why an app is broken, and what changed
  kubexray changes             list recent changes and who made them
  kubexray dump                stream the cluster graph as JSON lines
  kubexray version             print the version

Run "kubexray <command> -h" for a command's flags.
kubexray only reads from the cluster. It never changes anything.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "kubexray:", err)
		}
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// No command, or only flags: start the UI.
	if len(args) == 0 || (strings.HasPrefix(args[0], "-") && !slices.Contains(helpAndVersion, args[0])) {
		return ui(ctx, args, stdout, stderr)
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "why":
		return why(ctx, rest, stdout, stderr)
	case "incidents":
		return incidents(ctx, rest, stdout, stderr)
	case "changes":
		return changes(ctx, rest, stdout, stderr)
	case "dump":
		return dump(ctx, rest, stdout, stderr)
	case "version", "--version", "-version":
		_, err := fmt.Fprintln(stdout, version.String())
		return err
	case "help", "-h", "-help", "--help":
		_, err := fmt.Fprint(stdout, usage)
		return err
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

var (
	helpAndVersion = []string{"-h", "-help", "--help", "-version", "--version"}
	commands       = []string{"why", "incidents", "changes", "dump", "version", "help"}
)

// connection holds the flags every command that reads a cluster shares.
type connection struct {
	kubeconfig, context, namespace string
}

func (c *connection) flags(fs *flag.FlagSet, withNamespace bool) {
	fs.StringVar(&c.kubeconfig, "kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	fs.StringVar(&c.context, "context", "", "kubeconfig context to use (default: current context)")
	if withNamespace {
		fs.StringVar(&c.namespace, "n", "", "namespace (default: the context's namespace)")
		fs.StringVar(&c.namespace, "namespace", "", "namespace (default: the context's namespace)")
	}
}

// sync connects read-only and waits until every object has been seen, then
// returns the graph. It tells the user which cluster it is reading first.
func (c *connection) sync(ctx context.Context, log *slog.Logger, stderr io.Writer) (*graph.Graph, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = c.kubeconfig
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: c.context})
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	ns := c.namespace
	if ns == "" {
		if ns, _, err = cc.Namespace(); err != nil {
			return nil, "", fmt.Errorf("reading namespace from kubeconfig: %w", err)
		}
	}
	name := c.context
	if name == "" {
		name = raw.CurrentContext
	}
	_, _ = fmt.Fprintf(stderr, "Reading cluster %q at %s (read-only)…\n", name, cfg.Host)

	cfg.UserAgent = "kubexray/" + version.Version
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("creating client: %w", err)
	}
	metaClient, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("creating metadata client: %w", err)
	}
	g := graph.New()
	if err := cluster.New(client, metaClient, g, log).Start(ctx); err != nil {
		return nil, "", err
	}
	return g, ns, nil
}

// state syncs and returns the cluster as a graph state.
func (c *connection) state(ctx context.Context, stderr io.Writer) (*graph.State, string, error) {
	quiet := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	g, ns, err := c.sync(ctx, quiet, stderr)
	if err != nil {
		return nil, "", err
	}
	_, _, snap := g.FlushAndSnapshot()
	st, err := graph.NewState(snap)
	return st, ns, err
}

func why(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("why", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var conn connection
	conn.flags(fs, true)
	output := fs.String("o", "text", "output format: text or json")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: kubexray why <kind>/<name> [flags]\n\nExplains why an app is broken and what changed, e.g. kubexray why deploy/api -n shop.\n\nFlags:")
		fs.PrintDefaults()
	}
	// Accept the target before or after the flags: kubexray why deploy/api -n
	// shop, or kubexray why -n shop deploy/api.
	target := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if target == "" {
		target = fs.Arg(0)
	}
	if fs.NArg() > 1 || (target != fs.Arg(0) && fs.NArg() > 0) {
		return fmt.Errorf("expected one app, got extra arguments %v", fs.Args())
	}
	if target == "" {
		fs.Usage()
		return errors.New("which app? for example: kubexray why deploy/api")
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("-o must be text or json, got %q", *output)
	}

	st, ns, err := conn.state(ctx, stderr)
	if err != nil {
		return err
	}
	id, err := cli.ParseTarget(target, ns)
	if err != nil {
		return err
	}
	now := time.Now()
	inc, err := cause.Explain(st, id, cause.DefaultOptions(now))
	if err != nil {
		return err
	}
	if *output == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(inc)
	}
	return cli.Why(stdout, inc, now)
}

func incidents(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("incidents", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var conn connection
	conn.flags(fs, false)
	output := fs.String("o", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("-o must be text or json, got %q", *output)
	}
	st, _, err := conn.state(ctx, stderr)
	if err != nil {
		return err
	}
	now := time.Now()
	list := cause.Analyze(st, cause.DefaultOptions(now))
	groups := cause.GroupIncidents(list)
	if *output == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Incidents []cause.Incident `json:"incidents"`
			Groups    []cause.Group    `json:"groups"`
		}{list, groups})
	}
	return cli.Incidents(stdout, list, groups, now)
}

func changes(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("changes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var conn connection
	conn.flags(fs, true)
	since := fs.Duration("since", time.Hour, "how far back to look")
	all := fs.Bool("all", false, "include changes made by Kubernetes' own controllers")
	allNS := fs.Bool("A", false, "all namespaces")
	output := fs.String("o", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *since <= 0 {
		return fmt.Errorf("--since must be positive, got %v", *since)
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("-o must be text or json, got %q", *output)
	}

	st, ns, err := conn.state(ctx, stderr)
	if err != nil {
		return err
	}
	now := time.Now()
	list := cause.Changes(st, now.Add(-*since), *all)
	if !*allNS {
		kept := list[:0]
		for _, c := range list {
			if c.NS == ns || c.NS == "" {
				kept = append(kept, c)
			}
		}
		list = kept
	}
	if *output == "json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	return cli.Changes(stdout, list, now, *since)
}

// historyFlags are shared by the commands that record.
type historyFlags struct {
	window time.Duration
	mb     int
}

func (h *historyFlags) register(fs *flag.FlagSet) {
	fs.DurationVar(&h.window, "history", recorder.DefaultOptions.Window, "how far back to keep history for rewind")
	fs.IntVar(&h.mb, "history-max-mb", recorder.DefaultOptions.MaxBytes>>20, "memory budget for history, in MiB")
}

func (h *historyFlags) recorder() (*recorder.Recorder, error) {
	if h.window <= 0 {
		return nil, fmt.Errorf("--history must be positive, got %v", h.window)
	}
	if h.mb <= 0 || h.mb > maxHistoryMB {
		return nil, fmt.Errorf("--history-max-mb must be between 1 and %d, got %d", maxHistoryMB, h.mb)
	}
	return recorder.New(recorder.Options{Window: h.window, MaxBytes: h.mb << 20, KeyframeEvery: recorder.DefaultOptions.KeyframeEvery}), nil
}

// ui syncs the cluster, starts recording and serves the web UI on 127.0.0.1.
func ui(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("kubexray", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var conn connection
	conn.flags(fs, false)
	var hist historyFlags
	hist.register(fs)
	port := fs.Int("port", 0, "port to listen on, on 127.0.0.1 (default: a free port)")
	noBrowser := fs.Bool("no-browser", false, "don't open the browser")
	uiDir := fs.String("ui-dir", "", "serve the UI from this directory instead of the built-in one (for UI development)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		if slices.Contains(commands, fs.Arg(0)) {
			return fmt.Errorf("put flags after the command, e.g. kubexray %s ... --context <name>", fs.Arg(0))
		}
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	}
	rec, err := hist.recorder()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hub := server.NewHub(server.MaxClients, server.ClientQueue)
	assets := web.Assets()
	if *uiDir != "" {
		assets = os.DirFS(*uiDir)
	}
	srv, err := server.New(hub, rec, assets, log)
	if err != nil {
		return err
	}
	// Bind first, so a busy --port fails before a long cluster sync.
	ln, err := srv.Listen(ctx, *port)
	if err != nil {
		return fmt.Errorf("listening: %w", err)
	}
	g, _, err := conn.sync(ctx, log, stderr)
	if err != nil {
		_ = ln.Close()
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	run := func(f func() error) {
		wg.Go(func() {
			if err := f(); err != nil {
				errs <- err
			}
			cancel() // either one stopping stops both
		})
	}
	run(func() error { return recorder.Pump(ctx, g, rec, flushInterval, log, hub.Emit) })
	run(func() error { return srv.Serve(ctx, ln) })

	// Open the browser only once the first snapshot is ready to stream.
	select {
	case <-hub.Ready():
		_, _ = fmt.Fprintf(stdout, "kubexray is running at %s   (Ctrl-C to stop)\n", srv.URL())
		if !*noBrowser {
			if err := openBrowser(ctx, srv.URL()); err != nil {
				_, _ = fmt.Fprintf(stderr, "Couldn't open the browser (%v); open the link above.\n", err)
			}
		}
	case <-ctx.Done():
	}

	wg.Wait() // let the server shut down gracefully
	close(errs)
	return <-errs
}

// openBrowser opens url, kubexray's own launch link, with the platform's
// default browser.
func openBrowser(ctx context.Context, url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url) //nolint:gosec // url is our own launch link
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec // url is our own launch link
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", url) //nolint:gosec // url is our own launch link
	}
	return cmd.Start()
}

func dump(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var conn connection
	conn.flags(fs, false)
	var hist historyFlags
	hist.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rec, err := hist.recorder()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	g, _, err := conn.sync(ctx, log, stderr)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil // interrupted during startup
		}
		return err
	}
	return recorder.Pump(ctx, g, rec, flushInterval, log, ndjson(stdout))
}

// ndjson writes each message as one line, as soon as it is produced.
func ndjson(w io.Writer) recorder.Emit {
	return func(_ graph.Message, raw []byte) error {
		// raw is shared with the recorder: the full slice expression makes
		// append copy instead of writing into its spare capacity.
		_, err := w.Write(append(raw[:len(raw):len(raw)], '\n'))
		return err
	}
}
