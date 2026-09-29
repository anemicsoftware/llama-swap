package peerdiscovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/config"
)

const listingJSON = `{"object":"list","data":[
 {"id":"vision-model","meta":{"llamaswap":{"type":"model"},"n_ctx":65536},
  "architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},
  "capabilities":{"vision":true,"function_calling":true},"context_length":131072},
 {"id":"plain","meta":{"llamaswap":{"type":"model"}}},
 {"id":"an-alias","meta":{"llamaswap":{"type":"alias"}}},
 {"id":"other/peer-model","meta":{"llamaswap":{"type":"peer"}}},
 {"id":"llama-server-model","meta":{"n_ctx":8192}}
]}`

func peerFor(t *testing.T, rawURL string) config.PeerConfig {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return config.PeerConfig{Proxy: rawURL, ProxyURL: u, Discover: true}
}

func TestPeerDiscovery_ParseListing(t *testing.T) {
	res, err := parseListing([]byte(listingJSON))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"llama-server-model", "plain", "vision-model"}; !reflect.DeepEqual(res.models, want) {
		t.Fatalf("models = %v, want %v", res.models, want)
	}
	vision := res.caps["vision-model"]
	if !reflect.DeepEqual(vision.In, []string{"text", "image"}) || !vision.Tools || vision.Context != 131072 {
		t.Errorf("vision-model caps = %+v", vision)
	}
	if got := res.caps["llama-server-model"].Context; got != 8192 {
		t.Errorf("llama-server-model context = %d, want 8192", got)
	}
	if _, ok := res.caps["plain"]; ok {
		t.Errorf("plain should have no capabilities, got %+v", res.caps["plain"])
	}
}

func TestPeerDiscovery_ApplyMergesModelsAndCapabilities(t *testing.T) {
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(listingJSON))
	}))
	defer srv.Close()

	peer := peerFor(t, srv.URL)
	peer.ApiKey = "secret"
	peer.Models = []string{"static", "plain"}
	peer.Capabilities = map[string]config.ModelCapConfig{"vision-model": {Context: 4096}}
	cfg := config.Config{Peers: config.PeerDictionaryConfig{"p": peer}}

	New(nil).Apply(context.Background(), &cfg)

	got := cfg.Peers["p"]
	if want := []string{"static", "plain", "llama-server-model", "vision-model"}; !reflect.DeepEqual(got.Models, want) {
		t.Errorf("models = %v, want %v", got.Models, want)
	}
	if gotAuth.Load() != "Bearer secret" {
		t.Errorf("Authorization = %v", gotAuth.Load())
	}
	caps := got.ModelCapabilities("vision-model")
	if caps.Context != 4096 || !caps.Tools || len(caps.In) != 2 {
		t.Errorf("configured context should win, discovered fields fill the rest: %+v", caps)
	}
}

func TestPeerDiscovery_KeepsLastGoodResultWhenPeerFails(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(listingJSON))
	}))
	defer srv.Close()

	d := New(nil)
	first := config.Config{Peers: config.PeerDictionaryConfig{"p": peerFor(t, srv.URL)}}
	d.Apply(context.Background(), &first)

	fail.Store(true)
	second := config.Config{Peers: config.PeerDictionaryConfig{"p": peerFor(t, srv.URL)}}
	d.Apply(context.Background(), &second)
	if len(second.Peers["p"].Models) != 3 {
		t.Errorf("models after failed refresh = %v, want the previous 3", second.Peers["p"].Models)
	}
	if d.Changed(context.Background()) {
		t.Error("an unreachable peer must not count as changed")
	}
}

func TestPeerDiscovery_Changed(t *testing.T) {
	var body atomic.Value
	body.Store(listingJSON)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()

	d := New(nil)
	cfg := config.Config{Peers: config.PeerDictionaryConfig{"p": peerFor(t, srv.URL)}}
	d.Apply(context.Background(), &cfg)
	if d.Changed(context.Background()) {
		t.Fatal("Changed() = true right after Apply")
	}
	body.Store(`{"data":[{"id":"new-model"}]}`)
	if !d.Changed(context.Background()) {
		t.Fatal("Changed() = false after the peer's models changed")
	}
}

func TestPeerDiscovery_DropsDiscoveredModelsThatBreakNamespace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"clash"}]}`))
	}))
	defer srv.Close()

	// "p/clash" is reserved as the fully qualified name of the peer model.
	cfg := config.Config{
		Models: map[string]config.ModelConfig{"p/clash": {}},
		Peers:  config.PeerDictionaryConfig{"p": peerFor(t, srv.URL)},
	}
	New(nil).Apply(context.Background(), &cfg)
	if len(cfg.Peers["p"].Models) != 0 {
		t.Errorf("models = %v, want discovery result dropped", cfg.Peers["p"].Models)
	}
}
