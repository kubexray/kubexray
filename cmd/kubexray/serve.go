package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kubexray/kubexray/internal/alert"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/recorder"
	"github.com/kubexray/kubexray/internal/server"
	"github.com/kubexray/kubexray/internal/version"
	"github.com/kubexray/kubexray/internal/web"
)

// hostList collects a repeatable flag.
type hostList []string

func (h *hostList) String() string     { return strings.Join(*h, ",") }
func (h *hostList) Set(v string) error { *h = append(*h, v); return nil }

// serve records around the clock and serves the UI to the network, behind
// authentication (ADR-0010). It's what the Helm chart runs.
func serve(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var conn connection
	conn.flags(fs, false)
	listen := fs.String("listen", ":8080", "address to listen on")
	auth := fs.String("auth", "token", `"token", or "none" for an auth proxy in front (only with a loopback --listen)`)
	tokenFile := fs.String("token-file", "", "file holding the access token (default: $KUBEXRAY_TOKEN)")
	var hosts hostList
	fs.Var(&hosts, "allowed-host", "a host name the UI is reached at, e.g. an Ingress's (repeatable)")
	secure := fs.Bool("secure-cookie", false, "mark the session cookie Secure: kubexray is behind TLS")
	dataDir := fs.String("data-dir", "", "keep history on disk in this directory, so it survives restarts")
	retention := fs.Duration("retention", 7*24*time.Hour, "how long to keep history")
	retentionMB := fs.Int64("retention-size-mb", 1024, "disk budget for history, in MiB (with --data-dir)")
	fs.BoolVar(&conn.noSecrets, "no-secrets", false, "don't watch Secrets, when kubexray isn't allowed to list them")
	clusterName := fs.String("cluster-name", "", `the cluster's name in replays (default: the kubeconfig context, or "in-cluster")`)
	publicURL := fs.String("public-url", "", "where people open kubexray, e.g. https://kubexray.example.com, for links in alerts")
	slackFile := fs.String("slack-webhook-file", "", "file holding a Slack incoming webhook URL: send Alertmanager alerts there with the likely cause")
	webhookFile := fs.String("webhook-url-file", "", "file holding a webhook URL: send Alertmanager alerts there as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("serve takes no arguments, got %q", fs.Arg(0))
	}

	remote := server.Remote{Addr: *listen, AllowedHosts: hosts, SecureCookie: *secure}
	switch *auth {
	case "none":
		remote.NoAuth = true
	case "token":
		tok, err := readToken(*tokenFile)
		if err != nil {
			return err
		}
		remote.Token, remote.TokenFile = tok, *tokenFile
	default:
		return fmt.Errorf(`--auth must be "token" or "none", got %q`, *auth)
	}
	if err := remote.Check(); err != nil {
		return err
	}
	slackURL, err := readURL(*slackFile, true)
	if err != nil {
		return fmt.Errorf("--slack-webhook-file: %w", err)
	}
	webhookURL, err := readURL(*webhookFile, false)
	if err != nil {
		return fmt.Errorf("--webhook-url-file: %w", err)
	}
	if *publicURL != "" && !validURL(*publicURL, false) {
		return fmt.Errorf("--public-url must be an http(s) URL, got %q", *publicURL)
	}
	if *retention <= 0 || *retentionMB <= 0 {
		return errors.New("--retention and --retention-size-mb must be positive")
	}

	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	opts := recorder.Options{
		Window: *retention, MaxBytes: recorder.DefaultOptions.MaxBytes, KeyframeEvery: recorder.DefaultOptions.KeyframeEvery,
		Dir: *dataDir, MaxDiskBytes: *retentionMB << 20,
	}
	var rec *recorder.Recorder
	if *dataDir != "" {
		var err error
		if rec, err = recorder.Open(opts); err != nil {
			return err
		}
		defer func() {
			if err := rec.Close(); err != nil {
				log.Error("saving the last history segment", "err", err)
			}
		}()
	} else {
		rec = recorder.New(opts)
	}

	if err := conn.resolve(stderr); err != nil {
		return err
	}
	name := *clusterName
	if name == "" {
		name = conn.name
	}
	if name == "" {
		name = "in-cluster"
	}
	hub := server.NewHub(server.MaxClients, server.ClientQueue)
	// The change feed of what was recorded before this start (ADR-0012),
	// rebuilt in the background while recording goes on.
	go func() {
		history := feed.New(feed.MaxSets)
		if err := feed.Rebuild(history, rec.Walk); err != nil {
			log.Warn("rebuilding the change feed from history", "err", err)
			return
		}
		hub.Feed().Seed(history)
	}()
	srv, err := server.New(hub, rec, web.Assets(), log, server.Info{Cluster: name, Version: version.Version})
	if err != nil {
		return err
	}
	if notifier := alert.NewNotifier(slackURL, webhookURL, log); notifier.Enabled() {
		srv.SetAlerts(notifier, *publicURL)
		go notifier.Run(ctx)
	}
	ln, err := srv.ListenRemote(ctx, remote)
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
	return recordAndServe(ctx, g, rec, hub, srv, ln, log, func() {
		from, _, _ := rec.Bounds()
		log.Info("kubexray is serving", "addr", ln.Addr().String(), "cluster", name, "history_from", from, "secrets", !conn.noSecrets)
	})
}

// readURL reads a destination URL from a file (a mounted Secret: the URL is
// a credential). Slack's must be https.
func readURL(path string, httpsOnly bool) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // the operator's flag (a mounted Secret)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(b))
	if !validURL(raw, httpsOnly) {
		return "", errors.New("not a valid URL") // without echoing it: it's a credential
	}
	return raw, nil
}

// validURL reports whether raw is an absolute https URL, or http too unless
// httpsOnly.
func validURL(raw string, httpsOnly bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "https" || (!httpsOnly && u.Scheme == "http"))
}

// readToken reads the access token from a file (a mounted Secret) or the
// environment.
func readToken(path string) (string, error) {
	tok := os.Getenv("KUBEXRAY_TOKEN")
	if path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // the operator's --token-file (a mounted Secret)
		if err != nil {
			return "", fmt.Errorf("reading the token: %w", err)
		}
		tok = string(b)
	}
	tok = strings.TrimSpace(tok)
	if len(tok) < server.MinTokenLength {
		return "", fmt.Errorf("serve needs an access token of at least %d characters in --token-file or $KUBEXRAY_TOKEN (or --auth=none behind an auth proxy)", server.MinTokenLength)
	}
	return tok, nil
}
