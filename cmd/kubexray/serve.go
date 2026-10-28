package main

import (
	"bytes"
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

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubexray/kubexray/internal/alert"
	"github.com/kubexray/kubexray/internal/auth"
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
	authMode := fs.String("auth", "token", `"token"; "oidc" to sign people in with an OpenID Connect provider; or "none" for an auth proxy in front (only with a loopback --listen)`)
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
	var oidcFlags oidcOptions
	oidcFlags.flags(fs)
	valuesAccess := fs.String("values-access", "all", `who sees ConfigMap and environment values: "all" signed-in people, or "kubernetes": only those Kubernetes lets read ConfigMaps in the namespace (needs --auth=oidc; ADR-0020)`)
	kubeUserPrefix := fs.String("kube-username-prefix", "", "what the API server puts before OIDC usernames (its --oidc-username-prefix), with --values-access=kubernetes")
	kubeGroupsPrefix := fs.String("kube-groups-prefix", "", "what the API server puts before OIDC groups (its --oidc-groups-prefix), with --values-access=kubernetes")
	var recOpts recommendOptions
	recOpts.flags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("serve takes no arguments, got %q", fs.Arg(0))
	}

	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	remote := server.Remote{Addr: *listen, AllowedHosts: hosts, SecureCookie: *secure}
	switch *authMode {
	case "none":
		remote.NoAuth = true
	case "token", "oidc":
		tok, err := readToken(*tokenFile)
		if err != nil {
			return err
		}
		remote.Token, remote.TokenFile = tok, *tokenFile
	default:
		return fmt.Errorf(`--auth must be "token", "oidc" or "none", got %q`, *authMode)
	}
	if *authMode == "oidc" {
		o, err := oidcFlags.signIn(ctx, *publicURL, log)
		if err != nil {
			return err
		}
		remote.OIDC = o
		// People open kubexray at the public URL.
		if u, err := url.Parse(*publicURL); err == nil {
			remote.AllowedHosts = append(remote.AllowedHosts, u.Host)
			remote.SecureCookie = remote.SecureCookie || u.Scheme == "https"
		}
	} else if oidcFlags.set() {
		return errors.New("the --oidc-* and --allow-* flags need --auth=oidc")
	}
	switch {
	case *valuesAccess != "all" && *valuesAccess != "kubernetes":
		return fmt.Errorf(`--values-access must be "all" or "kubernetes", got %q`, *valuesAccess)
	case *valuesAccess == "kubernetes" && *authMode != "oidc":
		return errors.New("--values-access=kubernetes needs --auth=oidc: Kubernetes permissions belong to people, and only OIDC sign-in says who someone is")
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
	svc, err := recOpts.service(&conn, log)
	if err != nil {
		return err
	}
	if svc != nil {
		srv.SetRecommender(svc)
	}
	if *valuesAccess == "kubernetes" {
		access, err := kubeAccess(ctx, &conn, *kubeUserPrefix, *kubeGroupsPrefix)
		if err != nil {
			return err
		}
		srv.SetValuesAccess(access)
	}
	if notifier := alert.NewNotifier(slackURL, webhookURL, log); notifier.Enabled() {
		srv.SetAlerts(notifier, *publicURL)
		go notifier.Run(ctx)
	}
	ln, err := srv.ListenRemote(ctx, remote)
	if err != nil {
		return fmt.Errorf("listening: %w", err)
	}
	conn.history = rec.Walk // baselines survive a restart
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

// oidcOptions are serve's sign-in flags (ADR-0013).
type oidcOptions struct {
	issuer, clientID, secretFile, groupsClaim, keyFile string
	usernameClaim                                      string
	scopes, emails, domains, groups                    hostList
	ttl                                                time.Duration
}

func (o *oidcOptions) flags(fs *flag.FlagSet) {
	fs.StringVar(&o.issuer, "oidc-issuer", "", "the OpenID Connect provider, e.g. https://accounts.google.com (with --auth=oidc)")
	fs.StringVar(&o.clientID, "oidc-client-id", "", "kubexray's client ID at the provider")
	fs.StringVar(&o.secretFile, "oidc-client-secret-file", "", "file holding the client secret (a mounted Secret)")
	fs.Var(&o.scopes, "oidc-scope", "a scope to ask for besides openid, email and profile, e.g. groups (repeatable)")
	fs.StringVar(&o.groupsClaim, "oidc-groups-claim", "groups", "the ID token claim listing someone's groups")
	fs.StringVar(&o.usernameClaim, "oidc-username-claim", "email", "the ID token claim the API server takes as the username (its --oidc-username-claim), with --values-access=kubernetes")
	fs.Var(&o.emails, "allow-email", "let this email address in (repeatable)")
	fs.Var(&o.domains, "allow-domain", "let everyone with a verified email in this domain in (repeatable)")
	fs.Var(&o.groups, "allow-group", "let members of this group in (repeatable)")
	fs.StringVar(&o.keyFile, "session-key-file", "", "file holding the key that signs sessions, at least 32 bytes (a mounted Secret)")
	fs.DurationVar(&o.ttl, "session-ttl", 12*time.Hour, "how long a sign-in lasts")
}

// set reports whether any sign-in flag was given.
func (o *oidcOptions) set() bool {
	return o.issuer != "" || o.clientID != "" || o.secretFile != "" || o.keyFile != "" ||
		len(o.scopes)+len(o.emails)+len(o.domains)+len(o.groups) > 0
}

func (o *oidcOptions) signIn(ctx context.Context, publicURL string, log *slog.Logger) (*auth.OIDC, error) {
	if o.secretFile == "" || o.keyFile == "" {
		return nil, errors.New("--auth=oidc needs --oidc-client-secret-file and --session-key-file")
	}
	secret, err := os.ReadFile(o.secretFile)
	if err != nil {
		return nil, fmt.Errorf("reading the OIDC client secret: %w", err)
	}
	key, err := os.ReadFile(o.keyFile)
	if err != nil {
		return nil, fmt.Errorf("reading the session key: %w", err)
	}
	return auth.NewOIDC(ctx, auth.Config{
		Issuer: o.issuer, ClientID: o.clientID, ClientSecret: strings.TrimSpace(string(secret)),
		PublicURL: publicURL, Scopes: o.scopes, GroupsClaim: o.groupsClaim, UsernameClaim: o.usernameClaim,
		Rules:      auth.Rules{Emails: o.emails, Domains: o.domains, Groups: o.groups},
		SessionKey: bytes.TrimSpace(key), SessionTTL: o.ttl,
	}, log)
}

// kubeAccess checks values against Kubernetes permissions (ADR-0020), once
// kubexray itself may ask: it needs to create SubjectAccessReviews.
func kubeAccess(ctx context.Context, conn *connection, userPrefix, groupsPrefix string) (*server.KubeAccess, error) {
	client, err := kubernetes.NewForConfig(conn.cfg)
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}
	self := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authorizationv1.ResourceAttributes{Group: "authorization.k8s.io", Resource: "subjectaccessreviews", Verb: "create"},
	}}
	res, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, self, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("--values-access=kubernetes: checking kubexray's own permissions: %w", err)
	}
	if !res.Status.Allowed {
		return nil, errors.New("--values-access=kubernetes: kubexray may not create SubjectAccessReviews; the Helm chart grants it with diffs.access: kubernetes")
	}
	return server.NewKubeAccess(client, userPrefix, groupsPrefix), nil
}
