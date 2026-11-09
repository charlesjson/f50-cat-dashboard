package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

func activeConfig(s State) (Object, error) {
	if s.Active == "current" {
		return parseYAML(s.Base)
	}
	for _, source := range s.Sources {
		if source.ID == s.Active && source.Mode == "full" {
			return parseYAML(source.Config)
		}
	}
	return nil, errors.New("找不到已选完整配置")
}

func baseGroupID(name string) string { return "base-" + hash([]byte(name))[:24] }

type groupResolver func(string) (string, error)

// Edits are scoped to the active full profile. Preserve unedited upstream groups
// and apply changes again when a subscription refresh replaces its source YAML.
func applyGroupEdits(cfg Object, s State) ([]Object, groupResolver, map[string]bool, error) {
	edits := s.GroupEdits[s.Active]
	byOriginal := map[string]GroupEdit{}
	for _, edit := range edits {
		if edit.Original != "" {
			byOriginal[edit.Original] = edit
		}
	}
	groups := []Object{}
	seen := map[string]bool{}
	modified := map[string]bool{}
	for _, group := range objects(cfg["proxy-groups"]) {
		name := stringOf(group["name"])
		edit, exists := byOriginal[name]
		if !exists {
			groups = append(groups, group)
			continue
		}
		seen[edit.ID] = true
		if !edit.Deleted {
			groups = append(groups, clone(edit.Config))
			modified[stringOf(edit.Config["name"])] = true
		}
	}
	for _, edit := range edits {
		if !seen[edit.ID] && !edit.Deleted {
			groups = append(groups, clone(edit.Config))
			modified[stringOf(edit.Config["name"])] = true
		}
	}
	owners := map[string]string{}
	for _, node := range objects(cfg["proxies"]) {
		owners[stringOf(node["name"])] = "node:" + stringOf(node["name"])
	}
	for _, group := range groups {
		name := stringOf(group["name"])
		if owners[name] != "" {
			return nil, nil, nil, fmt.Errorf("策略组名称重复: %s", name)
		}
		owners[name] = baseGroupID(name)
	}
	for _, edit := range edits {
		if !edit.Deleted {
			owners[stringOf(edit.Config["name"])] = edit.ID
		}
	}
	aliases := map[string]string{}
	aliasOwners := map[string]string{}
	for _, edit := range edits {
		target := stringOf(edit.Config["name"])
		if edit.Deleted {
			target = edit.Replacement
		}
		for _, name := range append([]string{edit.Original}, edit.Aliases...) {
			if name == "" {
				continue
			}
			if owner := owners[name]; owner != "" && owner != edit.ID {
				return nil, nil, nil, fmt.Errorf("名称 %s 被其他策略组的改名记录占用", name)
			}
			if owner := aliasOwners[name]; owner != "" && owner != edit.ID {
				return nil, nil, nil, fmt.Errorf("策略组的历史名称冲突: %s", name)
			}
			aliasOwners[name] = edit.ID
			if name != target {
				aliases[name] = target
			}
		}
	}
	resolve := func(name string) (string, error) {
		visited := map[string]bool{}
		for {
			if visited[name] {
				return "", fmt.Errorf("策略组替代目标存在循环: %s", name)
			}
			visited[name] = true
			target, exists := aliases[name]
			if !exists {
				return name, nil
			}
			if target == "" {
				return "", fmt.Errorf("请为已删除的策略组 %s 选择替代目标", name)
			}
			name = target
		}
	}
	if err := mapGroupReferences(cfg, groups, resolve); err != nil {
		return nil, nil, nil, err
	}
	return groups, resolve, modified, nil
}

// Rule conditions may contain commas; only the final policy token (before an
// optional no-resolve) refers to a proxy. SUB-RULE refers to a sub-rule name.
func mapRuleTarget(rule string, resolve groupResolver) (string, error) {
	parts := strings.Split(rule, ",")
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "SUB-RULE" {
		return rule, nil
	}
	i := len(parts) - 1
	if strings.TrimSpace(parts[i]) == "no-resolve" {
		i--
	}
	if i < 1 {
		return rule, nil
	}
	old := strings.TrimSpace(parts[i])
	target, err := resolve(old)
	if err != nil {
		return "", err
	}
	if target == old {
		return rule, nil
	}
	parts[i] = target
	return strings.Join(parts, ","), nil
}

func mapDNSReference(value string, resolve groupResolver) (string, error) {
	parts := strings.SplitN(value, "#", 2)
	if len(parts) != 2 {
		return value, nil
	}
	params := strings.Split(parts[1], "&")
	for i, param := range params {
		mapped, err := resolve(param)
		if err != nil {
			return "", err
		}
		params[i] = mapped
	}
	return parts[0] + "#" + strings.Join(params, "&"), nil
}

func mapGroupReferences(cfg Object, groups []Object, resolve groupResolver) error {
	for _, group := range groups {
		members := stringsOf(group["proxies"])
		if group["proxies"] != nil {
			mapped, affected := []string{}, map[string]bool{}
			for _, member := range members {
				name, err := resolve(member)
				if err != nil {
					return err
				}
				mapped = append(mapped, name)
				if name != member {
					affected[name] = true
				}
			}
			result, seen := []string{}, map[string]bool{}
			for _, name := range mapped {
				if affected[name] && seen[name] {
					continue
				}
				result = append(result, name)
				seen[name] = true
			}
			group["proxies"] = result
		}
	}
	for _, node := range objects(cfg["proxies"]) {
		if old := stringOf(node["dialer-proxy"]); old != "" {
			name, err := resolve(old)
			if err != nil {
				return err
			}
			node["dialer-proxy"] = name
		}
	}
	for _, key := range []string{"proxy-providers", "rule-providers"} {
		for _, value := range objectOf(cfg[key]) {
			provider := objectOf(value)
			if old := stringOf(provider["proxy"]); old != "" {
				name, err := resolve(old)
				if err != nil {
					return err
				}
				provider["proxy"] = name
			}
		}
	}
	mapRules := func(v any) ([]string, error) {
		result := []string{}
		for _, rule := range stringsOf(v) {
			mapped, err := mapRuleTarget(rule, resolve)
			if err != nil {
				return nil, err
			}
			result = append(result, mapped)
		}
		return result, nil
	}
	if cfg["rules"] != nil {
		rules, err := mapRules(cfg["rules"])
		if err != nil {
			return err
		}
		cfg["rules"] = rules
	}
	for name, value := range objectOf(cfg["sub-rules"]) {
		rules, err := mapRules(value)
		if err != nil {
			return err
		}
		objectOf(cfg["sub-rules"])[name] = rules
	}
	var walk func(any) (any, error)
	walk = func(value any) (any, error) {
		switch item := value.(type) {
		case string:
			return mapDNSReference(item, resolve)
		case []any:
			for i, v := range item {
				mapped, err := walk(v)
				if err != nil {
					return nil, err
				}
				item[i] = mapped
			}
		case []string:
			for i, v := range item {
				mapped, err := mapDNSReference(v, resolve)
				if err != nil {
					return nil, err
				}
				item[i] = mapped
			}
		case map[string]any:
			for k, v := range item {
				mapped, err := walk(v)
				if err != nil {
					return nil, err
				}
				item[k] = mapped
			}
		}
		return value, nil
	}
	if _, err := walk(cfg["dns"]); err != nil {
		return err
	}
	if old := stringOf(objectOf(cfg["ntp"])["proxy"]); old != "" {
		name, err := resolve(old)
		if err != nil {
			return err
		}
		objectOf(cfg["ntp"])["proxy"] = name
	}
	return nil
}

func validGroup(group Object) error {
	name := strings.TrimSpace(stringOf(group["name"]))
	if name == "" || len(name) > 256 || strings.ContainsAny(name, ",\r\n\x00") {
		return errors.New("策略组名称不能为空、超过 256 字节或包含逗号/换行")
	}
	group["name"] = name
	for _, reserved := range []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"} {
		if name == reserved {
			return errors.New("不能使用内置策略名称")
		}
	}
	kind := stringOf(group["type"])
	if kind != "select" && kind != "url-test" && kind != "fallback" && kind != "load-balance" {
		return errors.New("支持手动选择、自动选择、故障转移和负载均衡策略组")
	}
	if len(stringsOf(group["proxies"])) == 0 && len(stringsOf(group["use"])) == 0 && group["include-all"] != true && group["include-all-proxies"] != true && group["include-all-providers"] != true {
		return errors.New("请至少选择一个成员或启用自动包含节点")
	}
	if kind != "select" {
		if validSourceURL(stringOf(group["url"])) != nil {
			return errors.New("请输入合法的 HTTP/HTTPS 测试地址")
		}
		interval, err := numberInRange(group["interval"], 1, 604800)
		if err != nil {
			return errors.New("测试间隔必须是 1 到 604800 秒的整数")
		}
		group["interval"] = interval
	}
	if kind == "url-test" && group["tolerance"] != nil {
		value, err := numberInRange(group["tolerance"], 0, 10000)
		if err != nil {
			return errors.New("延迟容差必须是 0 到 10000 毫秒的整数")
		}
		group["tolerance"] = value
	}
	if kind == "load-balance" {
		strategy := stringOf(group["strategy"])
		if strategy != "consistent-hashing" && strategy != "round-robin" && strategy != "sticky-sessions" {
			return errors.New("请选择有效的负载均衡策略")
		}
	}
	return nil
}

func numberInRange(value any, min, max int) (int, error) {
	var number float64
	switch v := value.(type) {
	case int:
		number = float64(v)
	case float64:
		number = v
	default:
		return 0, errors.New("不是整数")
	}
	if number < float64(min) || number > float64(max) || number != float64(int(number)) {
		return 0, errors.New("整数超出范围")
	}
	return int(number), nil
}

func validateGroupGraph(cfg Object, groups []Object) error {
	known := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true}
	edges := map[string][]string{}
	for _, node := range objects(cfg["proxies"]) {
		known[stringOf(node["name"])] = true
	}
	for _, group := range groups {
		name := stringOf(group["name"])
		if name == "" || known[name] || name == "GLOBAL" {
			return fmt.Errorf("策略组名称为空、重复或与节点冲突: %s", name)
		}
		known[name] = true
		edges[name] = stringsOf(group["proxies"])
		for _, provider := range stringsOf(group["use"]) {
			if _, ok := objectOf(cfg["proxy-providers"])[provider]; !ok {
				return fmt.Errorf("策略组 %s 的节点集合不存在: %s", name, provider)
			}
		}
	}
	for _, node := range objects(cfg["proxies"]) {
		if via := stringOf(node["dialer-proxy"]); via != "" {
			edges[stringOf(node["name"])] = []string{via}
		}
	}
	status := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if status[name] == 1 {
			return fmt.Errorf("策略组或链式代理存在循环引用: %s", name)
		}
		if status[name] == 2 {
			return nil
		}
		status[name] = 1
		for _, target := range edges[name] {
			if !known[target] {
				return fmt.Errorf("%s 引用了不存在或已停用的成员: %s", name, target)
			}
			if err := visit(target); err != nil {
				return err
			}
		}
		status[name] = 2
		return nil
	}
	for name := range edges {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func groupViews(s State, cfg Object) []GroupView {
	result := []GroupView{}
	for _, group := range objects(cfg["proxy-groups"]) {
		name := stringOf(group["name"])
		view := GroupView{ID: baseGroupID(name), Config: clone(group), References: []string{}}
		for _, edit := range s.GroupEdits[s.Active] {
			if !edit.Deleted && stringOf(edit.Config["name"]) == name {
				view.ID = edit.ID
				view.Modified = true
				view.Custom = edit.Original == ""
			}
		}
		for _, other := range objects(cfg["proxy-groups"]) {
			for _, member := range stringsOf(other["proxies"]) {
				if member == name {
					view.References = append(view.References, "策略组："+stringOf(other["name"]))
					break
				}
			}
		}
		for _, node := range objects(cfg["proxies"]) {
			if node["dialer-proxy"] == name {
				view.References = append(view.References, "链式节点："+stringOf(node["name"]))
			}
		}
		rules := 0
		counter := func(target string) (string, error) {
			if target == name {
				rules++
			}
			return target, nil
		}
		for _, rule := range stringsOf(cfg["rules"]) {
			_, _ = mapRuleTarget(rule, counter)
		}
		for _, list := range objectOf(cfg["sub-rules"]) {
			for _, rule := range stringsOf(list) {
				_, _ = mapRuleTarget(rule, counter)
			}
		}
		if rules > 0 {
			view.References = append(view.References, fmt.Sprintf("分流规则：%d 条", rules))
		}
		result = append(result, view)
	}
	return result
}

func updateGroup(s *State, action Action) error {
	if s.Mode != "full" {
		return errors.New("请先切换为完整模式，再编辑策略组")
	}
	data, _, err := compile(*s)
	if err != nil {
		return err
	}
	cfg, err := parseYAML(string(data))
	if err != nil {
		return err
	}
	var current *GroupView
	for _, view := range groupViews(*s, cfg) {
		if view.ID == action.ID {
			copy := view
			current = &copy
			break
		}
	}
	if action.ID != "" && current == nil {
		return errors.New("策略组不存在，请刷新后重试")
	}
	if action.Op == "delete-group" && current == nil {
		return errors.New("请选择要删除的策略组")
	}
	if s.GroupEdits == nil {
		s.GroupEdits = map[string][]GroupEdit{}
	}
	edits := s.GroupEdits[s.Active]
	index := -1
	for i, edit := range edits {
		if edit.ID == action.ID {
			index = i
			break
		}
	}
	edit := GroupEdit{ID: "custom-" + newID()}
	if index >= 0 {
		edit = edits[index]
	} else if current != nil {
		edit.ID = current.ID
		edit.Original = stringOf(current.Config["name"])
	}
	if action.Op == "delete-group" {
		if action.Replacement == "" || action.Replacement == stringOf(current.Config["name"]) {
			return errors.New("请选择不同的删除后替代目标")
		}
		found := false
		for _, name := range append([]string{"DIRECT", "REJECT", "REJECT-DROP"}, append(stringsOfMemberNames(cfg["proxies"]), stringsOfMemberNames(cfg["proxy-groups"])...)...) {
			if name == action.Replacement {
				found = true
			}
		}
		if !found {
			return errors.New("替代目标不存在")
		}
		edit.Aliases = appendUnique(edit.Aliases, stringOf(current.Config["name"]))
		edit.Deleted, edit.Replacement, edit.Config = true, action.Replacement, nil
	} else {
		group := clone(action.Group)
		if err := validGroup(group); err != nil {
			return err
		}
		if current != nil && current.Config["name"] != group["name"] {
			edit.Aliases = appendUnique(edit.Aliases, stringOf(current.Config["name"]))
		}
		edit.Config, edit.Deleted, edit.Replacement = group, false, ""
	}
	if index >= 0 {
		edits[index] = edit
	} else {
		edits = append(edits, edit)
	}
	s.GroupEdits[s.Active] = edits
	return nil
}

func appendUnique(list []string, name string) []string {
	for _, value := range list {
		if value == name {
			return list
		}
	}
	return append(list, name)
}

func stringsOfMemberNames(value any) []string {
	names := []string{}
	for _, item := range objects(value) {
		names = append(names, stringOf(item["name"]))
	}
	return names
}

func providerNames(cfg Object) []string {
	names := []string{}
	for name := range objectOf(cfg["proxy-providers"]) {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
