// Package peerdiscovery asks peers which models they serve.
//
// A peer with discover: true is queried with GET /v1/models. The listing
// supplies the model IDs and, when the peer reports them, the same
// capability fields llama-swap renders itself: architecture modalities,
// capabilities, supported_parameters and context_length. Peers that report
// nothing more than IDs (older llama-swap builds, for example) still get
// their models routed; their capabilities can be set in the peer's
// capabilities block.
package peerdiscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mostlygeek/llama-swap/internal/config"
)

const requestTimeout = 10 * time.Second

// Logger receives discovery warnings.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
}

type result struct {
	models []string
	caps   map[string]config.ModelCapConfig
}

// Discoverer applies discovery results to configs. It remembers the last
// good result per peer so a peer that is briefly unreachable during a reload
// keeps the models it had.
type Discoverer struct {
	logger Logger
	client *http.Client

	mu      sync.Mutex
	last    map[string]result
	targets map[string]config.PeerConfig
}

// New returns a Discoverer. logger may be nil.
func New(logger Logger) *Discoverer {
	return &Discoverer{
		logger:  logger,
		client:  &http.Client{Timeout: requestTimeout},
		last:    make(map[string]result),
		targets: make(map[string]config.PeerConfig),
	}
}

// Apply queries every peer with discover set and merges the answer into
// cfg.Peers: discovered IDs are appended to Models and capabilities stored in
// DiscoveredCapabilities. Peers that cannot be reached fall back to the last
// good result. If the merged peer models would make the config invalid, the
// discovered models are dropped rather than failing the load.
func (d *Discoverer) Apply(ctx context.Context, cfg *config.Config) {
	d.mu.Lock()
	d.targets = make(map[string]config.PeerConfig)
	for id, peer := range cfg.Peers {
		if peer.Discover {
			d.targets[id] = peer
		}
	}
	d.mu.Unlock()

	original := cfg.Peers
	merged := make(config.PeerDictionaryConfig, len(original))
	for id, peer := range original {
		merged[id] = peer
	}
	for id, peer := range original {
		if !peer.Discover {
			continue
		}
		res, err := d.fetch(ctx, id, peer)
		d.mu.Lock()
		if err != nil {
			d.warnf("peer %s: model discovery failed: %v", id, err)
			if prev, ok := d.last[id]; ok {
				res = prev
			}
		} else {
			d.last[id] = res
		}
		d.mu.Unlock()
		merged[id] = withResult(peer, res)
	}

	cfg.Peers = merged
	if err := config.ValidatePeerNamespace(*cfg); err != nil {
		d.warnf("discovered peer models rejected: %v", err)
		cfg.Peers = original
	}
}

// Changed reports whether any peer's live model list or capabilities differ
// from the last result Apply used. Unreachable peers count as unchanged.
func (d *Discoverer) Changed(ctx context.Context) bool {
	d.mu.Lock()
	targets := make(map[string]config.PeerConfig, len(d.targets))
	for id, peer := range d.targets {
		targets[id] = peer
	}
	d.mu.Unlock()

	for id, peer := range targets {
		res, err := d.fetch(ctx, id, peer)
		if err != nil {
			continue
		}
		d.mu.Lock()
		prev, ok := d.last[id]
		d.mu.Unlock()
		if !ok || !reflect.DeepEqual(prev, res) {
			return true
		}
	}
	return false
}

func withResult(peer config.PeerConfig, res result) config.PeerConfig {
	seen := make(map[string]struct{}, len(peer.Models)+len(res.models))
	models := make([]string, 0, len(peer.Models)+len(res.models))
	for _, id := range append(append([]string{}, peer.Models...), res.models...) {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			models = append(models, id)
		}
	}
	peer.Models = models
	peer.DiscoveredCapabilities = res.caps
	return peer
}

// listing is the subset of a /v1/models entry discovery reads. It accepts
// what llama-swap renders and the common fields other servers publish.
type listing struct {
	Data []struct {
		ID           string `json:"id"`
		Architecture struct {
			Input  []string `json:"input_modalities"`
			Output []string `json:"output_modalities"`
		} `json:"architecture"`
		Capabilities        map[string]any `json:"capabilities"`
		SupportedParameters []string       `json:"supported_parameters"`
		ContextLength       int            `json:"context_length"`
		ContextWindow       int            `json:"context_window"`
		MaxModelLen         int            `json:"max_model_len"`
		Meta                struct {
			NCtx      int `json:"n_ctx"`
			LlamaSwap struct {
				Type string `json:"type"`
			} `json:"llamaswap"`
		} `json:"meta"`
	} `json:"data"`
}

func (d *Discoverer) fetch(ctx context.Context, id string, peer config.PeerConfig) (result, error) {
	if peer.ProxyURL == nil {
		return result{}, fmt.Errorf("peer has no proxy URL")
	}
	if strings.EqualFold(peer.ProxyURL.Scheme, "tailcat") {
		return result{}, fmt.Errorf("discover is not supported for tailcat peers; list models explicitly")
	}
	endpoint := *peer.ProxyURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/models"
	endpoint.RawQuery = ""

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointString(endpoint), nil)
	if err != nil {
		return result{}, err
	}
	if peer.ApiKey != "" {
		req.Header.Set("Authorization", "Bearer "+peer.ApiKey)
		req.Header.Set("x-api-key", peer.ApiKey)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result{}, fmt.Errorf("GET %s: %s", endpointString(endpoint), resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return result{}, err
	}
	return parseListing(body)
}

func endpointString(u url.URL) string { return u.String() }

func parseListing(body []byte) (result, error) {
	var l listing
	if err := json.Unmarshal(body, &l); err != nil {
		return result{}, fmt.Errorf("parsing model listing: %w", err)
	}
	res := result{caps: make(map[string]config.ModelCapConfig)}
	for _, m := range l.Data {
		if m.ID == "" {
			continue
		}
		// Aliases duplicate a model, and a peer's own peers or selectors are
		// not something this server can route through it by these names.
		switch m.Meta.LlamaSwap.Type {
		case "", "model":
		default:
			continue
		}
		res.models = append(res.models, m.ID)

		caps := config.ModelCapConfig{
			In:       m.Architecture.Input,
			Out:      m.Architecture.Output,
			Tools:    boolCap(m.Capabilities, "function_calling") || hasParam(m.SupportedParameters, "tools"),
			Reranker: boolCap(m.Capabilities, "reranker"),
		}
		if len(caps.In) == 0 && boolCap(m.Capabilities, "vision") {
			caps.In = []string{"text", "image"}
		}
		for _, n := range []int{m.ContextLength, m.ContextWindow, m.MaxModelLen, m.Meta.NCtx} {
			if n > 0 {
				caps.Context = n
				break
			}
		}
		caps.In, caps.Out = knownModalities(caps.In), knownModalities(caps.Out)
		if !caps.Empty() {
			res.caps[m.ID] = caps
		}
	}
	sort.Strings(res.models)
	return res, nil
}

func boolCap(caps map[string]any, key string) bool {
	v, _ := caps[key].(bool)
	return v
}

func hasParam(params []string, name string) bool {
	for _, p := range params {
		if p == name {
			return true
		}
	}
	return false
}

// knownModalities drops modalities llama-swap does not recognise so one odd
// value from a peer cannot make the whole capability block invalid.
func knownModalities(in []string) []string {
	var out []string
	for _, m := range in {
		if (config.ModelCapConfig{In: []string{m}}).Validate() == nil {
			out = append(out, m)
		}
	}
	return out
}

func (d *Discoverer) warnf(format string, args ...any) {
	if d.logger != nil {
		d.logger.Warnf(format, args...)
	}
}
