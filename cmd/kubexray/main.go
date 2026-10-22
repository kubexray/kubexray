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
	"os/signal"
	"strings"
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
	"github.com/kubexray/kubexray/internal/version"
)

// flushInterval is how often changes are batched into a delta (ADR-0003).
const flushInterval = 100 * time.Millisecond

// maxHistoryMB bounds --history-max-mb (64 GiB) well below int overflow.
const maxHistoryMB = 64 << 10

const usage = `kubexray is the flight recorder for your Kubernetes cluster.

Usage:
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
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return flag.ErrHelp
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd, rest := args[0], args[1:]; cmd {
	case "why":
		return why(ctx, rest, stdout, stderr)
	case "changes":
		return changes(ctx, rest, stdout, stderr)
	case "dump":
		return dump(ctx, rest, stdout, stderr)
	case "version", "--version", "-version":
		_, err := fmt.Fprintln(stdout, version.String())
		return err
	case "help", "-h", "--help":
		_, err := fmt.Fprint(stdout, usage)
		return err
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

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

func dump(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var conn connection
	conn.flags(fs, false)
	history := fs.Duration("history", recorder.DefaultOptions.Window, "how far back to keep history for rewind")
	historyMB := fs.Int("history-max-mb", recorder.DefaultOptions.MaxBytes>>20, "memory budget for history, in MiB")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *history <= 0 {
		return fmt.Errorf("--history must be positive, got %v", *history)
	}
	if *historyMB <= 0 || *historyMB > maxHistoryMB {
		return fmt.Errorf("--history-max-mb must be between 1 and %d, got %d", maxHistoryMB, *historyMB)
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	g, _, err := conn.sync(ctx, log, stderr)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil // interrupted during startup
		}
		return err
	}
	rec := recorder.New(recorder.Options{
		Window:        *history,
		MaxBytes:      *historyMB << 20,
		KeyframeEvery: recorder.DefaultOptions.KeyframeEvery,
	})
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
