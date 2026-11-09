package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

var pinnedKeys = []string{"external-controller", "external-controller-tls", "external-controller-unix", "external-ui", "external-ui-url", "secret", "tproxy-port", "redir-port", "tun", "cmfa-plugin"}

var advancedKeys = map[string]bool{
	"mixed-port": true, "port": true, "socks-port": true,
	"allow-lan": true, "ipv6": true, "mode": true, "log-level": true,
	"unified-delay": true, "tcp-concurrent": true,
	"keep-alive-idle": true, "keep-alive-interval": true,
}

type NodeView struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
}

func compile(s State) ([]byte, []NodeView, error) {
	cfg, err := activeConfig(s)
	if err != nil {
		return nil, nil, err
	}
	if s.Mode != "simple" && s.Mode != "full" {
		return nil, nil, errors.New("未知应用模式")
	}
	disabled := map[string]bool{}
	for _, name := range s.Disabled {
		disabled[name] = true
	}
	all := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true}
	var nodes []Object
	var views []NodeView
	var additions []string
	add := func(node Object, source string, enabled, additional bool) error {
		name := strings.TrimSpace(stringOf(node["name"]))
		if name == "" || strings.ContainsAny(name, ",\r\n") || all[name] {
			return fmt.Errorf("节点名称为空、含逗号或重复: %s", name)
		}
		all[name] = true
		available := enabled
		enabled = enabled && !disabled[name]
		views = append(views, NodeView{name, stringOf(node["type"]), source, enabled, available})
		if enabled {
			nodes = append(nodes, clone(node))
			if additional {
				additions = append(additions, name)
			}
		}
		return nil
	}
	for _, node := range objects(cfg["proxies"]) {
		if err := add(node, "当前配置", true, false); err != nil {
			return nil, nil, err
		}
	}
	for _, source := range s.Sources {
		if source.Mode != "pool" {
			continue
		}
		// Stable source IDs avoid node-name collisions between subscriptions.
		renamed := map[string]string{}
		for _, node := range source.Nodes {
			renamed[stringOf(node["name"])] = source.Name + " · " + stringOf(node["name"])
		}
		for _, original := range source.Nodes {
			node := clone(original)
			node["name"] = renamed[stringOf(node["name"])]
			if via, ok := renamed[stringOf(node["dialer-proxy"])]; ok {
				node["dialer-proxy"] = via
			}
			if err := add(node, source.Name, source.Enabled, true); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, item := range s.Manual {
		if err := add(item.Node, "手动节点", item.Enabled, true); err != nil {
			return nil, nil, err
		}
	}
	// A chain uses a copy of its exit node; the original node remains unchanged.
	chainNames := map[string]bool{}
	for _, chain := range s.Chains {
		if all[chain.Name] || chainNames[chain.Name] || strings.TrimSpace(chain.Name) == "" || strings.ContainsAny(chain.Name, ",\r\n") {
			return nil, nil, errors.New("链式代理名称为空或重复")
		}
		chainNames[chain.Name] = true
	}
	pending := append([]Chain(nil), s.Chains...)
	for len(pending) > 0 {
		var next []Chain
		progress := false
		for _, chain := range pending {
			if !chain.Enabled || disabled[chain.Name] {
				views = append(views, NodeView{chain.Name, "chain", "链式代理", false, chain.Enabled})
				progress = true
				continue
			}
			var exit Object
			viaExists := false
			for _, node := range nodes {
				if stringOf(node["name"]) == chain.Exit {
					exit = node
				}
				viaExists = viaExists || stringOf(node["name"]) == chain.Via
			}
			if exit == nil || !viaExists {
				next = append(next, chain)
				continue
			}
			if chain.Via == chain.Exit {
				return nil, nil, errors.New("入口和出口不能相同")
			}
			node := clone(exit)
			node["name"], node["dialer-proxy"] = chain.Name, chain.Via
			if err := add(node, "链式代理", true, true); err != nil {
				return nil, nil, err
			}
			progress = true
		}
		if !progress {
			return nil, nil, errors.New("链式代理存在循环，或依赖的节点已删除/停用")
		}
		pending = next
	}
	if len(nodes) > 10000 {
		return nil, nil, errors.New("最多支持 10000 个节点")
	}
	cfg["proxies"] = nodes
	groups := objects(cfg["proxy-groups"])
	resolveGroup := groupResolver(func(name string) (string, error) { return name, nil })
	if s.Mode == "simple" {
		var names []string
		for _, node := range nodes {
			names = append(names, stringOf(node["name"]))
		}
		groups = []Object{{"name": "节点选择", "type": "select", "proxies": append(names, "DIRECT")}}
		cfg["rules"] = []string{"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,172.16.0.0/12,DIRECT,no-resolve", "IP-CIDR,192.168.0.0/16,DIRECT,no-resolve", "MATCH,节点选择"}
		delete(cfg, "proxy-providers")
		delete(cfg, "rule-providers")
	} else {
		var modified map[string]bool
		groups, resolveGroup, modified, err = applyGroupEdits(cfg, s)
		if err != nil {
			return nil, nil, err
		}
		for _, group := range groups {
			var names []string
			for _, name := range stringsOf(group["proxies"]) {
				if !disabled[name] {
					names = append(names, name)
				}
			}
			if len(names) == 0 && len(stringsOf(group["use"])) == 0 && group["include-all"] != true && group["include-all-proxies"] != true && group["include-all-providers"] != true {
				names = []string{"DIRECT"}
			}
			if len(names) > 0 {
				group["proxies"] = names
			}
		}
		if len(additions) > 0 {
			for _, group := range groups {
				if group["type"] == "select" {
					if !modified[stringOf(group["name"])] {
						group["proxies"] = append(stringsOf(group["proxies"]), additions...)
					}
					break
				}
			}
		}
		if len(groups) == 0 && len(nodes) > 0 && len(s.GroupEdits[s.Active]) == 0 {
			var names []string
			for _, node := range nodes {
				names = append(names, stringOf(node["name"]))
			}
			groups = []Object{{"name": "节点选择", "type": "select", "proxies": append(names, "DIRECT")}}
			cfg["rules"] = []string{"MATCH,节点选择"}
		}
	}
	cfg["proxy-groups"] = groups
	if err := validateGroupGraph(cfg, groups); err != nil {
		return nil, nil, err
	}
	targets := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true}
	for _, node := range nodes {
		targets[stringOf(node["name"])] = true
	}
	for _, group := range groups {
		name := stringOf(group["name"])
		if targets[name] || all[name] {
			return nil, nil, fmt.Errorf("策略组名称重复: %s", name)
		}
		targets[name] = true
	}
	var rules []string
	for _, route := range s.Routes {
		target, err := resolveGroup(route.Target)
		if err != nil {
			return nil, nil, err
		}
		route.Target = target
		if err := validateRoute(route, targets); err != nil {
			return nil, nil, err
		}
		rule := route.Type + "," + strings.TrimSpace(route.Value) + "," + route.Target
		if route.NoResolve && strings.HasPrefix(route.Type, "IP-") {
			rule += ",no-resolve"
		}
		rules = append(rules, rule)
	}
	cfg["rules"] = append(rules, stringsOf(cfg["rules"])...)
	if s.DNS != nil {
		dns := objectOf(cfg["dns"])
		for key, value := range s.DNS {
			if key == "hosts" {
				cfg["hosts"] = value
			} else if key != "listen" {
				dns[key] = value
			}
		}
		cfg["dns"] = dns
	}
	if s.DNSListen != nil {
		dns := objectOf(cfg["dns"])
		dns["listen"] = s.DNSListen
		cfg["dns"] = dns
	}
	for key, value := range s.Advanced {
		if !advancedKeys[key] {
			return nil, nil, fmt.Errorf("不支持的高级字段: %s", key)
		}
		cfg[key] = value
	}
	for key, value := range s.Resources {
		if key != "geox-url" && key != "geo-auto-update" && key != "geo-update-interval" {
			return nil, nil, fmt.Errorf("不支持的资源字段: %s", key)
		}
		cfg[key] = value
	}
	// Keep the management endpoint and F50's transparent proxy routing reachable.
	for _, key := range pinnedKeys {
		delete(cfg, key)
		if value, ok := s.Pins[key]; ok {
			cfg[key] = clone(value)
		}
	}
	// Dashboard updates must use this project's release, not replace management UI.
	cfg["external-ui-url"] = "https://github.com/charlesjson/f50-cat-dashboard/releases/latest/download/dist.zip"
	if s.Mode == "full" {
		if err := mapGroupReferences(cfg, groups, resolveGroup); err != nil {
			return nil, nil, err
		}
	}
	data, err := marshalYAML(cfg)
	return data, views, err
}

func validateRoute(route Route, targets map[string]bool) error {
	allowed := map[string]bool{"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "IP-CIDR": true, "IP-CIDR6": true, "SRC-IP-CIDR": true, "DST-PORT": true, "SRC-PORT": true, "GEOIP": true, "GEOSITE": true, "RULE-SET": true}
	if !allowed[route.Type] || !targets[route.Target] || strings.TrimSpace(route.Value) == "" || strings.ContainsAny(route.Value+route.Target, ",\r\n") {
		return errors.New("路由类型、匹配内容或目标无效")
	}
	if strings.Contains(route.Type, "CIDR") {
		if _, _, err := net.ParseCIDR(route.Value); err != nil {
			return errors.New("IP 规则需要合法 CIDR，例如 192.0.2.0/24")
		}
	}
	return nil
}
