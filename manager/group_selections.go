package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type runtimeGroup struct {
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

func (a *App) runtimeGroups(ctx context.Context) (map[string]runtimeGroup, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.options.Controller+"/proxies", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+a.secret)
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("无法读取当前策略组选择，配置未保存")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取策略组选择失败: HTTP %d", response.StatusCode)
	}
	var data struct {
		Proxies map[string]runtimeGroup `json:"proxies"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxConfig)).Decode(&data); err != nil {
		return nil, errors.New("无法解析当前策略组状态")
	}
	if data.Proxies == nil {
		return nil, errors.New("Mihomo 未返回策略组状态")
	}
	return data.Proxies, nil
}

func (a *App) captureGroupSelections(ctx context.Context, next State) (map[string]string, error) {
	if a.options.Controller == "" || next.Active != a.state.Active || string(jsonBytes(next.GroupEdits[next.Active])) == string(jsonBytes(a.state.GroupEdits[a.state.Active])) {
		return nil, nil
	}
	runtime, err := a.runtimeGroups(ctx)
	if err != nil {
		return nil, err
	}
	selections := map[string]string{}
	for name, group := range runtime {
		if group.Type == "Selector" {
			selections[name] = group.Now
		}
	}
	return selections, nil
}

func resolveEditedName(s State, name string) (string, error) {
	seen := map[string]bool{}
	for {
		if seen[name] {
			return "", errors.New("策略组名称映射存在循环")
		}
		seen[name] = true
		matched := false
		for _, edit := range s.GroupEdits[s.Active] {
			alias := edit.Original == name
			for _, candidate := range edit.Aliases {
				alias = alias || candidate == name
			}
			if !alias {
				continue
			}
			target := stringOf(edit.Config["name"])
			if edit.Deleted {
				target = edit.Replacement
			}
			if target == name {
				return name, nil
			}
			name, matched = target, true
			break
		}
		if !matched {
			return name, nil
		}
	}
}

func (a *App) restoreGroupSelections(ctx context.Context, selections map[string]string, next *State) error {
	if len(selections) == 0 {
		return nil
	}
	runtime, err := a.runtimeGroups(ctx)
	if err != nil {
		return err
	}
	for name, target := range selections {
		if next != nil {
			deleted := false
			for _, edit := range next.GroupEdits[next.Active] {
				if !edit.Deleted {
					continue
				}
				deleted = deleted || edit.Original == name
				for _, alias := range edit.Aliases {
					deleted = deleted || alias == name
				}
			}
			if deleted {
				continue
			}
			name, err = resolveEditedName(*next, name)
			if err != nil {
				return err
			}
			target, err = resolveEditedName(*next, target)
			if err != nil {
				return err
			}
		}
		group, exists := runtime[name]
		if !exists || group.Type != "Selector" || group.Now == target {
			continue
		}
		allowed := false
		for _, member := range group.All {
			allowed = allowed || member == target
		}
		if !allowed {
			continue
		}
		if err := a.coreRequest(ctx, http.MethodPut, "/proxies/"+url.PathEscape(name), Object{"name": target}); err != nil {
			return fmt.Errorf("无法恢复策略组 %s 的节点选择: %w", name, err)
		}
	}
	return nil
}
