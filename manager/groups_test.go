package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func saveGroup(t *testing.T, a *App, id string, group Object) {
	t.Helper()
	if err := a.mutate(context.Background(), Action{Op: "save-group", Revision: a.state.Revision, ID: id, Group: group}); err != nil {
		t.Fatal(err)
	}
}

func groupWithName(t *testing.T, a *App, name string) GroupView {
	t.Helper()
	cfg, _ := mustCompile(t, a.state)
	for _, group := range groupViews(a.state, cfg) {
		if group.Config["name"] == name {
			return group
		}
	}
	t.Fatalf("missing group: %s", name)
	return GroupView{}
}

func TestGroupCreationEditingAndMemberOrder(t *testing.T) {
	a := testApp(t)
	saveGroup(t, a, "", Object{"name": "work", "type": "select", "proxies": []string{"DIRECT", "base"}, "icon": "https://example.org/icon.svg"})
	group := groupWithName(t, a, "work")
	if !group.Custom || !group.Modified {
		t.Fatal("custom group metadata missing")
	}
	group.Config["proxies"] = []string{"base", "DIRECT"}
	saveGroup(t, a, group.ID, group.Config)
	current := groupWithName(t, a, "work")
	if strings.Join(stringsOf(current.Config["proxies"]), ",") != "base,DIRECT" || current.Config["icon"] != "https://example.org/icon.svg" {
		t.Fatal("member order or advanced options were discarded")
	}
	for _, kind := range []string{"url-test", "fallback", "load-balance"} {
		group.Config = Object{"name": "work", "type": kind, "proxies": []string{"base", "DIRECT"}, "url": "https://example.org/check", "interval": 300, "tolerance": 50, "lazy": true, "strategy": "round-robin"}
		saveGroup(t, a, group.ID, group.Config)
		if groupWithName(t, a, "work").Config["type"] != kind {
			t.Fatal("group type did not persist")
		}
	}
}

func TestUneditedGroupsRetainOriginalMemberMultiplicity(t *testing.T) {
	a := testApp(t)
	a.state.Base = strings.Replace(testConfig, "proxies: [base, DIRECT]", "proxies: [base, DIRECT, base]", 1)
	cfg, _ := mustCompile(t, a.state)
	if strings.Join(stringsOf(objects(cfg["proxy-groups"])[0]["proxies"]), ",") != "base,DIRECT,base" {
		t.Fatal("opening the group editor normalized an unedited group")
	}
}

func TestGroupRenameUpdatesRulesDNSProvidersDialersAndHistory(t *testing.T) {
	a := testApp(t)
	a.state.Base += `sub-rules:
  select:
    - MATCH,select
proxy-providers:
  pool: {type: file, path: ./pool.yaml, proxy: select}
rule-providers:
  domains: {type: file, path: ./domains.yaml, behavior: domain, proxy: select}
`
	cfg, err := parseYAML(a.state.Base)
	if err != nil {
		t.Fatal(err)
	}
	cfg["proxies"] = append(objects(cfg["proxies"]), Object{"name": "dialer", "type": "ss", "server": "192.0.2.2", "port": 443, "dialer-proxy": "select"})
	cfg["proxy-groups"] = append(objects(cfg["proxy-groups"]), Object{"name": "parent", "type": "select", "proxies": []string{"select", "base"}})
	cfg["rules"] = []string{"AND,((DOMAIN-SUFFIX,example.org),(NETWORK,TCP)),select", "IP-CIDR,192.0.2.0/24,select,no-resolve", "SUB-RULE,(NETWORK,TCP),select", "MATCH,select"}
	data, _ := marshalYAML(cfg)
	a.state.Base = string(data)
	a.state.Routes = []Route{{Type: "DOMAIN", Value: "custom.invalid", Target: "select"}}
	a.state.DNS = Object{"nameserver": []string{"https://dns.example.org/dns-query#select&ecs=192.0.2.0/24"}}
	group := groupWithName(t, a, "select")
	group.Config["name"] = "renamed"
	saveGroup(t, a, group.ID, group.Config)
	// New references may use the current name. A second rename must retain them.
	saveGroup(t, a, "", Object{"name": "later", "type": "select", "proxies": []string{"renamed"}})
	group = groupWithName(t, a, "renamed")
	group.Config["name"] = "final"
	saveGroup(t, a, group.ID, group.Config)
	compiled, _ := mustCompile(t, a.state)
	rules := stringsOf(compiled["rules"])
	if rules[0] != "DOMAIN,custom.invalid,final" || rules[2] != "IP-CIDR,192.0.2.0/24,final,no-resolve" || rules[3] != "SUB-RULE,(NETWORK,TCP),select" || stringsOf(objectOf(compiled["sub-rules"])["select"])[0] != "MATCH,final" {
		t.Fatal("rule references were changed incorrectly")
	}
	if objects(compiled["proxies"])[1]["dialer-proxy"] != "final" || objectOf(objectOf(compiled["proxy-providers"])["pool"])["proxy"] != "final" || objectOf(objectOf(compiled["rule-providers"])["domains"])["proxy"] != "final" {
		t.Fatal("provider or dialer references were lost")
	}
	if stringsOf(objectOf(compiled["dns"])["nameserver"])[0] != "https://dns.example.org/dns-query#final&ecs=192.0.2.0/24" {
		t.Fatal("DNS group reference was lost")
	}
	if strings.Join(stringsOf(groupWithName(t, a, "later").Config["proxies"]), ",") != "final" {
		t.Fatal("references to the first renamed name were not retained")
	}
}

func TestGroupDeletionReplacesReferencesWithoutDiscardingRules(t *testing.T) {
	a := testApp(t)
	group := groupWithName(t, a, "select")
	if err := a.mutate(context.Background(), Action{Op: "delete-group", Revision: a.state.Revision, ID: group.ID, Replacement: "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := mustCompile(t, a.state)
	if len(objects(cfg["proxy-groups"])) != 0 || len(stringsOf(cfg["rules"])) != 2 || stringsOf(cfg["rules"])[1] != "MATCH,DIRECT" {
		t.Fatal("deleting the last group discarded original rules or reintroduced a group")
	}
}

func TestGroupCyclesNamesAndDeletionTargetsRejected(t *testing.T) {
	a := testApp(t)
	saveGroup(t, a, "", Object{"name": "child", "type": "select", "proxies": []string{"select"}})
	group := groupWithName(t, a, "select")
	tests := []Action{
		{Op: "save-group", ID: group.ID, Group: Object{"name": "select", "type": "select", "proxies": []string{"child"}}},
		{Op: "save-group", Group: Object{"name": "base", "type": "select", "proxies": []string{"DIRECT"}}},
		{Op: "save-group", Group: Object{"name": "DIRECT", "type": "select", "proxies": []string{"base"}}},
		{Op: "save-group", Group: Object{"name": "empty", "type": "select", "proxies": []string{}}},
		{Op: "save-group", Group: Object{"name": "unknown", "type": "select", "proxies": []string{"missing"}}},
		{Op: "save-group", Group: Object{"name": "bad-interval", "type": "url-test", "proxies": []string{"base"}, "url": "https://example.org", "interval": 0}},
		{Op: "delete-group", ID: group.ID},
		{Op: "delete-group", ID: group.ID, Replacement: "select"},
		{Op: "delete-group", ID: group.ID, Replacement: "child"},
	}
	for _, action := range tests {
		before := a.state.ConfigHash
		action.Revision = a.state.Revision
		if err := a.mutate(context.Background(), action); err == nil {
			t.Fatalf("invalid action accepted: %#v", action)
		}
		if a.state.ConfigHash != before {
			t.Fatal("invalid group edits modified the runtime configuration")
		}
	}
}

func TestGroupEditsAreScopedAndSurviveSubscriptionRefresh(t *testing.T) {
	a := testApp(t)
	a.state.Sources = []Source{{ID: "one", Name: "one", Mode: "full", Config: testConfig}, {ID: "two", Name: "two", Mode: "full", Config: testConfig}}
	a.state.Active = "one"
	group := groupWithName(t, a, "select")
	group.Config["name"] = "one-custom"
	saveGroup(t, a, group.ID, group.Config)
	if a.state.Sources[0].Config != testConfig {
		t.Fatal("editing a group rewrote the subscription source")
	}
	a.state.Sources[0].Config = strings.Replace(testConfig, "192.0.2.1", "192.0.2.10", 1)
	if groupWithName(t, a, "one-custom").Config["name"] != "one-custom" {
		t.Fatal("refresh overwrote the custom group")
	}
	a.state.Active = "two"
	if len(a.state.GroupEdits["two"]) != 0 || groupWithName(t, a, "select").Modified {
		t.Fatal("edits leaked into another subscription")
	}
	a.state.Active = "current"
	if groupWithName(t, a, "select").Modified {
		t.Fatal("edits leaked into the original device configuration")
	}
}

func TestGroupMembersStayExplicitAfterManualEdit(t *testing.T) {
	a := testApp(t)
	a.state.Manual = []ManualNode{{ID: "extra", Enabled: true, Node: Object{"name": "extra", "type": "ss", "server": "192.0.2.2", "port": 443}}}
	group := groupWithName(t, a, "select")
	if len(stringsOf(group.Config["proxies"])) != 3 {
		t.Fatal("legacy automatic node-pool membership changed")
	}
	group.Config["proxies"] = []string{"base"}
	saveGroup(t, a, group.ID, group.Config)
	if strings.Join(stringsOf(groupWithName(t, a, "select").Config["proxies"]), ",") != "base" {
		t.Fatal("node-pool automation undid the user's explicit member selection")
	}
	group = groupWithName(t, a, "select")
	group.Config["include-all-proxies"] = true
	saveGroup(t, a, group.ID, group.Config)
	if groupWithName(t, a, "select").Config["include-all-proxies"] != true {
		t.Fatal("dynamic member inclusion did not persist")
	}
}

func TestGroupViewListsReferencesAndPreservesInheritedOptions(t *testing.T) {
	a := testApp(t)
	group := groupWithName(t, a, "select")
	if len(group.References) != 1 || group.References[0] != "分流规则：1 条" {
		t.Fatal("group deletion impact was not reported")
	}
	group.Config["hidden"] = true
	group.Config["filter"] = "example"
	saveGroup(t, a, group.ID, group.Config)
	if groupWithName(t, a, "select").Config["hidden"] != true {
		t.Fatal("existing group options were erased")
	}
	if err := a.mutate(context.Background(), Action{Op: "mode", Revision: a.state.Revision, Mode: "simple"}); err != nil {
		t.Fatal(err)
	}
	if err := a.mutate(context.Background(), Action{Op: "save-group", Revision: a.state.Revision, Group: group.Config}); err == nil {
		t.Fatal("group editor silently changed the simplified template")
	}
}

func TestGroupSelectionMigrationDoesNotChangeReplacementGroups(t *testing.T) {
	a := testApp(t)
	runtime := map[string]runtimeGroup{
		"GLOBAL":      {Type: "Selector", Now: "DIRECT", All: []string{"renamed", "parent", "DIRECT"}},
		"renamed":     {Type: "Selector", Now: "DIRECT", All: []string{"base", "DIRECT"}},
		"parent":      {Type: "Selector", Now: "base", All: []string{"renamed", "DIRECT", "base"}},
		"replacement": {Type: "Selector", Now: "DIRECT", All: []string{"base", "DIRECT"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-controller-secret" {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/proxies" {
			writeJSON(w, 200, Object{"proxies": runtime})
			return
		}
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/proxies/") {
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			name := strings.TrimPrefix(r.URL.Path, "/proxies/")
			group := runtime[name]
			group.Now = body.Name
			runtime[name] = group
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	a.options.Controller = server.URL
	next := clone(a.state)
	next.GroupEdits = map[string][]GroupEdit{"current": {
		{ID: "one", Original: "old", Config: Object{"name": "renamed"}},
		{ID: "two", Original: "deleted", Deleted: true, Replacement: "replacement"},
	}}
	if err := a.restoreGroupSelections(context.Background(), map[string]string{"GLOBAL": "old", "old": "base", "parent": "old", "deleted": "base", "replacement": "DIRECT"}, &next); err != nil {
		t.Fatal(err)
	}
	if runtime["GLOBAL"].Now != "renamed" || runtime["renamed"].Now != "base" || runtime["parent"].Now != "renamed" || runtime["replacement"].Now != "DIRECT" {
		t.Fatal("group selection was lost or a deletion changed the replacement group's existing choice")
	}
}

func TestGroupSelectionMigrationFailureRollsBackOriginalSelection(t *testing.T) {
	a := testApp(t)
	runtime := map[string]runtimeGroup{"select": {Type: "Selector", Now: "base", All: []string{"DIRECT", "base"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/proxies" {
			writeJSON(w, 200, Object{"proxies": runtime})
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/proxies/renamed" {
			w.WriteHeader(500)
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/proxies/select" {
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			group := runtime["select"]
			group.Now = body.Name
			runtime["select"] = group
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	a.options.Controller = server.URL
	calls := 0
	a.reload = func(context.Context) error {
		calls++
		name := "renamed"
		if calls > 1 {
			name = "select"
		}
		runtime = map[string]runtimeGroup{name: {Type: "Selector", Now: "DIRECT", All: []string{"DIRECT", "base"}}}
		return nil
	}
	group := groupWithName(t, a, "select")
	group.Config["name"] = "renamed"
	err := a.mutate(context.Background(), Action{Op: "save-group", Revision: 1, ID: group.ID, Group: group.Config})
	if err == nil || !strings.Contains(err.Error(), "已恢复") || a.state.Revision != 1 || runtime["select"].Now != "base" {
		t.Fatal("a failed selection migration did not restore the original group and node choice")
	}
}
