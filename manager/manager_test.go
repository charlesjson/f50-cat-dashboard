package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testConfig = `mixed-port: 7890
external-controller: 0.0.0.0:7788
external-ui: WebUI/zashboard
secret: test-controller-secret
tproxy-port: 7893
dns:
  enable: true
  listen: 0.0.0.0:1053
  nameserver: [223.5.5.5]
proxies:
  - {name: base, type: ss, server: 192.0.2.1, port: 443, cipher: aes-128-gcm, password: example}
proxy-groups:
  - {name: select, type: select, proxies: [base, DIRECT]}
rules:
  - DOMAIN-SUFFIX,example.net,DIRECT
  - MATCH,select
`

func testApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(config, []byte(testConfig), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := openApp(Options{Root: filepath.Join(root, "manager"), Config: config, FastHash: filepath.Join(root, "fast_sha256")})
	if err != nil {
		t.Fatal(err)
	}
	a.validate = func(context.Context, []byte) error { return nil }
	a.reload = func(context.Context) error { return nil }
	return a
}

func mustCompile(t *testing.T, s State) (Object, []NodeView) {
	t.Helper()
	data, views, err := compile(s)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseYAML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, views
}

func TestFullProfileKeepsDeviceBindingsAndExistingRules(t *testing.T) {
	a := testApp(t)
	s := clone(a.state)
	s.Sources = []Source{{ID: "profile", Mode: "full", Config: strings.ReplaceAll(testConfig, "test-controller-secret", "foreign-secret") + "tun: {enable: true, auto-route: true}\n"}}
	s.Active = "profile"
	cfg, _ := mustCompile(t, s)
	if cfg["secret"] != "test-controller-secret" || cfg["external-controller"] != "0.0.0.0:7788" || cfg["tun"] != nil || objectOf(cfg["dns"])["listen"] != "0.0.0.0:1053" {
		t.Fatal("an imported profile changed the F50 transport or authentication")
	}
	if len(stringsOf(cfg["rules"])) != 2 || stringOf(cfg["external-ui-url"]) != "https://github.com/charlesjson/f50-cat-dashboard/releases/latest/download/dist.zip" {
		t.Fatal("existing routing or custom UI update source was lost")
	}
	s.DNS = Object{"hosts": Object{"example.invalid": "127.0.0.1"}, "listen": "0.0.0.0:9"}
	cfg, _ = mustCompile(t, s)
	if objectOf(cfg["hosts"])["example.invalid"] != "127.0.0.1" || objectOf(cfg["dns"])["listen"] != "0.0.0.0:1053" {
		t.Fatal("hosts entries or protected DNS listener were written at the wrong level")
	}
}

func TestMergeRenamesProvidersAndBuildsMultiHopChains(t *testing.T) {
	a := testApp(t)
	s := clone(a.state)
	for _, name := range []string{"A", "B"} {
		s.Sources = append(s.Sources, Source{ID: name, Name: name, Mode: "pool", Enabled: true, Nodes: []Object{{"name": "same", "type": "ss", "server": "192.0.2.2", "port": 443}}})
	}
	s.Chains = []Chain{{"2", "two-hop", "one-hop", "B · same", true}, {"1", "one-hop", "base", "A · same", true}}
	s.Routes = []Route{{Type: "DOMAIN-SUFFIX", Value: "example.org", Target: "two-hop"}}
	cfg, _ := mustCompile(t, s)
	nodes := objects(cfg["proxies"])
	if len(nodes) != 5 || nodes[4]["dialer-proxy"] != "one-hop" || stringsOf(cfg["rules"])[0] != "DOMAIN-SUFFIX,example.org,two-hop" {
		t.Fatal("merge, dependency order, or custom rule priority is incorrect")
	}
	if nodes[1]["dialer-proxy"] != nil {
		t.Fatal("chain creation mutated its original exit node")
	}
}

func TestCyclesDisabledDependenciesAndMalformedRoutesAreRejected(t *testing.T) {
	a := testApp(t)
	s := clone(a.state)
	s.Chains = []Chain{{"1", "A", "B", "base", true}, {"2", "B", "A", "base", true}}
	if _, _, err := compile(s); err == nil {
		t.Fatal("chain cycle accepted")
	}
	s.Chains = []Chain{{"1", "A", "base", "base", true}}
	if _, _, err := compile(s); err == nil {
		t.Fatal("self-chain accepted")
	}
	s.Chains = nil
	s.Routes = []Route{{Type: "IP-CIDR", Value: "not-a-cidr", Target: "DIRECT"}}
	if _, _, err := compile(s); err == nil {
		t.Fatal("malformed CIDR accepted")
	}
	s.Routes = nil
	s.Disabled = []string{"base"}
	cfg, _ := mustCompile(t, s)
	if len(objects(cfg["proxies"])) != 0 || strings.Join(stringsOf(objects(cfg["proxy-groups"])[0]["proxies"]), ",") != "DIRECT" {
		t.Fatal("disabled nodes remain selected by a group")
	}
}

func TestShareLinksAndBase64Subscriptions(t *testing.T) {
	vmess := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"ps":"vmess","add":"192.0.2.1","port":443,"id":"00000000-0000-4000-8000-000000000001","net":"ws","tls":"tls","path":"/ws","host":"example.org","sni":"example.org"}`))
	links := []string{vmess, "vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?security=reality&pbk=example&sid=12&fp=chrome#vless", "ss://" + base64.RawStdEncoding.EncodeToString([]byte("aes-128-gcm:example")) + "@192.0.2.3:443#ss", "trojan://example@192.0.2.4:443?sni=example.org#trojan", "hy2://example@192.0.2.5:443?obfs=salamander&obfs-password=example#hy2", "tuic://00000000-0000-4000-8000-000000000003:example@192.0.2.6:443#tuic"}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(links, "\n")))
	nodes, err := parseNodes(encoded)
	if err != nil || len(nodes) != 6 {
		t.Fatalf("could not import share links: %v", err)
	}
	if objectOf(nodes[0]["ws-opts"])["path"] != "/ws" || nodes[0]["tls"] != true || objectOf(nodes[1]["reality-opts"])["short-id"] != "12" || nodes[4]["obfs"] != "salamander" {
		t.Fatal("transport details were lost")
	}
	if _, err := parseNodes("ss://not-a-valid-link"); err == nil {
		t.Fatal("invalid sharing link accepted")
	}
}

func TestRuntimeFailureRollsBackConfigStateAndFastHash(t *testing.T) {
	a := testApp(t)
	calls := 0
	a.reload = func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("simulated runtime failure")
		}
		return nil
	}
	err := a.mutate(context.Background(), Action{Op: "advanced", Revision: 1, Values: Object{"mixed-port": 7891}})
	if err == nil || !strings.Contains(err.Error(), "已恢复") || calls != 2 || a.state.Revision != 1 {
		t.Fatal("runtime failure did not invoke recovery")
	}
	data, _ := os.ReadFile(a.options.Config)
	fastHash, _ := os.ReadFile(a.options.FastHash)
	if string(data) != testConfig || strings.TrimSpace(string(fastHash)) != hash(data) {
		t.Fatal("runtime config or startup hash was not restored")
	}
	if _, err := os.Stat(filepath.Join(a.options.Root, "pending.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful recovery left a pending transaction")
	}
}

func TestValidationAndStaleRevisionDoNotModifyDevice(t *testing.T) {
	a := testApp(t)
	a.validate = func(context.Context, []byte) error { return errors.New("invalid config") }
	for _, revision := range []int64{0, 1} {
		if a.mutate(context.Background(), Action{Op: "advanced", Revision: revision, Values: Object{"mixed-port": 7891}}) == nil {
			t.Fatal("stale revision or invalid config accepted")
		}
	}
	data, _ := os.ReadFile(a.options.Config)
	entries, _ := os.ReadDir(filepath.Join(a.options.Root, "backups"))
	if string(data) != testConfig || len(entries) != 0 {
		t.Fatal("validation failure changed persistent state")
	}
	_ = os.WriteFile(a.options.Config, []byte(testConfig+"# external edit\n"), 0600)
	if a.mutate(context.Background(), Action{Op: "reload", Revision: 1}) == nil {
		t.Fatal("external changes were silently overwritten")
	}
}

func TestSubscriptionFailureRetainsCachedNodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	source := Source{URL: server.URL, Mode: "pool", Nodes: []Object{{"name": "cached"}}, UpdatedAt: 123}
	if err := fetchSource(context.Background(), &source, ""); err == nil {
		t.Fatal("failed fetch was accepted")
	}
	if source.Nodes[0]["name"] != "cached" || source.UpdatedAt != 123 {
		t.Fatal("failed fetch destroyed cached nodes")
	}
}

func TestCurlPreservesSubscriptionQueryParameters(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is not installed")
	}
	query := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query <- r.URL.RawQuery
		_, _ = w.Write([]byte("proxies:\n  - {name: example, type: ss, server: 192.0.2.1, port: 443}\n"))
	}))
	defer server.Close()
	source := Source{URL: server.URL + "/nodes?token=example&format=clash", Mode: "pool"}
	if err := fetchSource(context.Background(), &source, curl); err != nil {
		t.Fatal(err)
	}
	if <-query != "token=example&format=clash" || len(source.Nodes) != 1 {
		t.Fatal("curl changed the subscription URL")
	}
}

func TestSlowScheduledUpdateDoesNotBlockOrOverwritePanelChanges(t *testing.T) {
	a := testApp(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte("proxies:\n  - {name: downloaded, type: ss, server: 192.0.2.1, port: 443}\n"))
	}))
	defer server.Close()
	a.state.Sources = []Source{{ID: "slow", Name: "slow", URL: server.URL, Mode: "pool", Enabled: true, IntervalHours: 1}}
	go func() { a.refreshDue(context.Background()); close(done) }()
	<-started
	changed := make(chan error, 1)
	go func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		changed <- a.mutate(context.Background(), Action{Op: "routes", Revision: 1, Rules: []Route{{Type: "DOMAIN", Value: "example.org", Target: "DIRECT"}}})
	}()
	select {
	case err := <-changed:
		if err != nil {
			close(release)
			<-done
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("background download blocked panel edits")
	}
	close(release)
	<-done
	if a.state.Revision != 2 || len(a.state.Routes) != 1 || len(a.state.Sources[0].Nodes) != 0 {
		t.Fatal("a stale background update overwrote newer panel state")
	}
}

func TestInactiveFullProfileIsValidatedBeforeCaching(t *testing.T) {
	a := testApp(t)
	a.validate = func(_ context.Context, data []byte) error {
		if strings.Contains(string(data), "invalid-protocol") {
			return errors.New("unsupported protocol")
		}
		return nil
	}
	content := strings.ReplaceAll(testConfig, "type: ss", "type: invalid-protocol")
	err := a.mutate(context.Background(), Action{Op: "save-source", Revision: 1, Name: "invalid", Mode: "full", Content: content, IntervalHours: 24})
	if err == nil || len(a.state.Sources) != 0 {
		t.Fatal("an invalid inactive profile entered the cache")
	}
}

func TestAPIRequiresLANOriginAndBearerHeader(t *testing.T) {
	a := testApp(t)
	for _, tc := range []struct {
		remote, origin, bearer, path string
		want                         int
	}{
		{"192.168.1.2:1234", "http://192.168.1.1:7788", "Bearer test-controller-secret", "/api/state", 200},
		{"192.168.1.2:1234", "https://example.org", "Bearer test-controller-secret", "/api/state", 403},
		{"192.168.1.2:1234", "", "", "/api/preview?secret=test-controller-secret", 401},
		{"203.0.113.2:1234", "", "Bearer test-controller-secret", "/api/state", 403},
	} {
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.1:7790"+tc.path, nil)
		req.RemoteAddr = tc.remote
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Authorization", tc.bearer)
		response := httptest.NewRecorder()
		a.handler("").ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Fatalf("access check got %d, want %d", response.Code, tc.want)
		}
	}
}

func TestInterruptedTransactionRecoversBeforeServing(t *testing.T) {
	a := testApp(t)
	old := snapshot{a.state, testConfig}
	_ = atomicWrite(filepath.Join(a.options.Root, "pending.json"), jsonBytes(old), 0600)
	_ = atomicWrite(a.options.Config, []byte("broken config"), 0600)
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
	defer server.Close()
	a.options.Controller = server.URL
	recovered, err := openApp(a.options)
	if err != nil || recovered.drift() || !called {
		t.Fatalf("interrupted transaction was not recovered: %v", err)
	}
	info, _ := os.Stat(filepath.Join(a.options.Root, "state.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("device state permissions expose credentials")
	}
}
