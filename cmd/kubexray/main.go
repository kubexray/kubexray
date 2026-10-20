// Command kubexray serves a live, animated map of a Kubernetes cluster.
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
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kubexray/kubexray/internal/cluster"
	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/version"
)

// flushInterval is how often changes are batched into a delta (ADR-0003).
const flushInterval = 100 * time.Millisecond

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kubexray:", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print version and exit")
	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	kubeContext := flag.String("context", "", "kubeconfig context to use (default: current context)")
	dump := flag.Bool("dump", false, "stream the cluster graph to stdout as NDJSON: a snapshot, then deltas")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return nil
	}
	if !*dump {
		return errors.New("the UI is not implemented yet; use --dump to stream the graph as JSON (see ROADMAP.md)")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, contextName, err := loadConfig(*kubeconfig, *kubeContext)
	if err != nil {
		return err
	}
	log.Info("connecting read-only", "context", contextName, "server", cfg.Host)
	cfg.UserAgent = "kubexray/" + version.Version

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	metaClient, err := metadata.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating metadata client: %w", err)
	}

	g := graph.New()
	if err := cluster.New(client, metaClient, g, log).Start(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil // interrupted during startup
		}
		return err
	}
	return dumpGraph(ctx, g, os.Stdout, flushInterval)
}

// loadConfig resolves the kubeconfig the way kubectl does and returns the
// name of the context in use.
func loadConfig(path, contextName string) (*rest.Config, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = path
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: contextName})

	raw, err := cc.RawConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}
	return cfg, contextName, nil
}

// dumpGraph writes a snapshot of the synced graph, then one delta per
// interval with changes, until ctx is cancelled.
func dumpGraph(ctx context.Context, g *graph.Graph, w io.Writer, interval time.Duration) error {
	enc := json.NewEncoder(w)
	g.Flush() // fold the initial sync into the snapshot
	if err := enc.Encode(g.Snapshot()); err != nil {
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if msg, ok := g.Flush(); ok {
				if err := enc.Encode(msg); err != nil {
					return err
				}
			}
		}
	}
}
