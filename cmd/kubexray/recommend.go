package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/kubexray/kubexray/internal/recommend"
)

// recommendOptions are the flags for safety recommendations (ADR-0014).
type recommendOptions struct {
	provider, model, url, keyFile string
}

// defaultLocalModelURL is Ollama's OpenAI-compatible endpoint.
const defaultLocalModelURL = "http://127.0.0.1:11434/v1"

func (o *recommendOptions) flags(fs *flag.FlagSet) {
	fs.StringVar(&o.provider, "recommend", "", `draft safety recommendations with a model: "anthropic", or "openai" for any OpenAI-compatible endpoint such as a local Ollama (off by default)`)
	fs.StringVar(&o.model, "recommend-model", "", "the model (default for anthropic: "+recommend.DefaultAnthropicModel+"; required for openai)")
	fs.StringVar(&o.url, "recommend-url", "", "the OpenAI-compatible endpoint (default: "+defaultLocalModelURL+", Ollama on this machine)")
	fs.StringVar(&o.keyFile, "recommend-key-file", "", "file holding the API key (default: $ANTHROPIC_API_KEY, or $OPENAI_API_KEY for openai)")
}

// service builds the recommendation service, or nil when it's off.
func (o *recommendOptions) service(conn *connection, log *slog.Logger) (*recommend.Service, error) {
	if o.provider == "" {
		if o.model != "" || o.url != "" || o.keyFile != "" {
			return nil, errors.New("the --recommend-* flags need --recommend=anthropic or --recommend=openai")
		}
		return nil, nil
	}
	key, err := o.key()
	if err != nil {
		return nil, err
	}
	var m recommend.Model
	switch o.provider {
	case "anthropic":
		if key == "" {
			return nil, errors.New("--recommend=anthropic needs an API key in --recommend-key-file or $ANTHROPIC_API_KEY")
		}
		m = recommend.NewAnthropic(key, o.model)
	case "openai":
		if o.model == "" {
			return nil, errors.New("--recommend=openai needs --recommend-model, e.g. qwen3-coder:30b")
		}
		url := o.url
		if url == "" {
			url = defaultLocalModelURL
		}
		if !validURL(url, false) {
			return nil, fmt.Errorf("--recommend-url must be an http(s) URL, got %q", url)
		}
		m = &recommend.OpenAI{BaseURL: url, APIKey: key, Model: o.model}
	default:
		return nil, fmt.Errorf(`--recommend must be "anthropic" or "openai", got %q`, o.provider)
	}
	client, err := kubernetes.NewForConfig(conn.cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(conn.cfg)
	if err != nil {
		return nil, err
	}
	return recommend.NewService(m, recommend.NewCluster(client, dyn), log), nil
}

// key reads the API key from the file, or the provider's variable.
func (o *recommendOptions) key() (string, error) {
	if o.keyFile != "" {
		b, err := os.ReadFile(o.keyFile)
		if err != nil {
			return "", fmt.Errorf("reading the recommendation API key: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if o.provider == "openai" {
		return os.Getenv("OPENAI_API_KEY"), nil
	}
	return os.Getenv("ANTHROPIC_API_KEY"), nil
}
