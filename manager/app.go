package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Root, Config, Core, Controller, Service, Curl, FastHash string
}

type App struct {
	mu       sync.Mutex
	options  Options
	state    State
	secret   string
	validate func(context.Context, []byte) error
	reload   func(context.Context) error
}

type snapshot struct {
	State  State  `json:"state"`
	Config string `json:"config"`
}

func openApp(o Options) (*App, error) {
	if err := os.MkdirAll(filepath.Join(o.Root, "backups"), 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(o.Root, 0700)
	a := &App{options: o}
	// Recover a write interrupted between the runtime config and state commits.
	pendingPath := filepath.Join(o.Root, "pending.json")
	recovered := false
	if pending, err := os.ReadFile(pendingPath); err == nil {
		var old snapshot
		if err := json.Unmarshal(pending, &old); err != nil {
			return nil, errors.New("事务恢复文件损坏，请先从备份恢复")
		}
		if err := atomicWrite(o.Config, []byte(old.Config), 0600); err != nil {
			return nil, err
		}
		if err := atomicWrite(filepath.Join(o.Root, "state.json"), jsonBytes(old.State), 0600); err != nil {
			return nil, err
		}
		recovered = true
	}
	current, err := os.ReadFile(o.Config)
	if err != nil {
		return nil, err
	}
	cfg, err := parseYAML(string(current))
	if err != nil {
		return nil, err
	}
	a.secret = stringOf(cfg["secret"])
	if a.secret == "" {
		return nil, errors.New("Mihomo 配置必须先设置非空 secret")
	}
	data, err := os.ReadFile(filepath.Join(o.Root, "state.json"))
	if err == nil {
		if err := json.Unmarshal(data, &a.state); err != nil {
			return nil, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		a.state = State{Revision: 1, Mode: "full", Active: "current", Base: string(current), Pins: Object{}, ConfigHash: hash(current)}
		for _, key := range pinnedKeys {
			if value, ok := cfg[key]; ok {
				a.state.Pins[key] = value
			}
		}
		a.state.DNSListen = objectOf(cfg["dns"])["listen"]
		if err := a.saveState(a.state); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	a.validate = a.validateCore
	a.reload = a.reloadCore
	if recovered {
		if err := a.updateFastHash(current); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if err := a.reload(ctx); err != nil {
			return nil, err
		}
		if err := os.Remove(pendingPath); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (a *App) saveState(state State) error {
	return atomicWrite(filepath.Join(a.options.Root, "state.json"), jsonBytes(state), 0600)
}

func (a *App) drift() bool {
	current, err := os.ReadFile(a.options.Config)
	return err != nil || hash(current) != a.state.ConfigHash
}

func (a *App) validateCore(ctx context.Context, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(a.options.Config), ".f50-check-*.yaml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	_ = f.Chmod(0600)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.options.Core, "-t", "-d", filepath.Dir(a.options.Config), "-f", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if len(message) > 1800 {
			message = message[len(message)-1800:]
		}
		return fmt.Errorf("Mihomo 校验未通过，当前配置未修改: %s", message)
	}
	return nil
}

func (a *App) coreRequest(ctx context.Context, method, path string, payload any) error {
	var body io.Reader
	if payload != nil {
		body = strings.NewReader(string(jsonBytes(payload)))
	}
	req, err := http.NewRequestWithContext(ctx, method, a.options.Controller+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.secret)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 35 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("无法连接 Mihomo 控制器")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Mihomo 返回 HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *App) reloadCore(ctx context.Context) error {
	return a.coreRequest(ctx, http.MethodPut, "/configs?force=true", Object{"path": a.options.Config})
}

func (a *App) updateFastHash(data []byte) error {
	if a.options.FastHash == "" {
		return nil
	}
	return atomicWrite(a.options.FastHash, []byte(hash(data)+"\n"), 0644)
}

func (a *App) backup(old snapshot) (string, error) {
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + newID()[:6] + ".json"
	path := filepath.Join(a.options.Root, "backups", name)
	if err := atomicWrite(path, jsonBytes(old), 0600); err != nil {
		return "", err
	}
	return name, nil
}

func (a *App) pruneBackups() {
	entries, _ := os.ReadDir(filepath.Join(a.options.Root, "backups"))
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for len(names) > 20 {
		_ = os.Remove(filepath.Join(a.options.Root, "backups", names[0]))
		names = names[1:]
	}
}

func (a *App) commit(ctx context.Context, next State) error {
	if len(next.Sources) > 32 || len(jsonBytes(next)) > 16<<20 {
		return errors.New("最多保存 32 个订阅，管理数据合计不得超过 16 MiB")
	}
	// Validate inactive full profiles too, before they enter the persistent cache.
	for _, source := range next.Sources {
		if source.Mode != "full" || source.ID == next.Active {
			continue
		}
		unchanged := false
		for _, previous := range a.state.Sources {
			if previous.ID == source.ID && previous.Config == source.Config {
				unchanged = true
			}
		}
		if unchanged {
			continue
		}
		probe := clone(next)
		probe.Active, probe.Mode = source.ID, "full"
		data, _, err := compile(probe)
		if err != nil {
			return err
		}
		if err := a.validate(ctx, data); err != nil {
			return err
		}
	}
	data, _, err := compile(next)
	if err != nil {
		return err
	}
	if err := a.validate(ctx, data); err != nil {
		return err
	}
	current, err := os.ReadFile(a.options.Config)
	if err != nil {
		return err
	}
	old := snapshot{a.state, string(current)}
	if _, err := a.backup(old); err != nil {
		return err
	}
	pendingPath := filepath.Join(a.options.Root, "pending.json")
	if err := atomicWrite(pendingPath, jsonBytes(old), 0600); err != nil {
		return err
	}
	next.Revision = a.state.Revision + 1
	next.ConfigHash = hash(data)
	rollback := func(cause error) error {
		configErr := atomicWrite(a.options.Config, current, 0600)
		hashErr := a.updateFastHash(current)
		stateErr := a.saveState(old.State)
		recoverCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		reloadErr := a.reload(recoverCtx)
		if configErr != nil || hashErr != nil || stateErr != nil || reloadErr != nil {
			return fmt.Errorf("应用失败，恢复未完成；已保留事务和备份文件: %w", cause)
		}
		_ = os.Remove(pendingPath)
		return fmt.Errorf("应用失败，已恢复原配置: %w", cause)
	}
	if err := atomicWrite(a.options.Config, data, 0600); err != nil {
		return rollback(err)
	}
	if err := a.updateFastHash(data); err != nil {
		return rollback(err)
	}
	if err := a.reload(ctx); err != nil {
		return rollback(err)
	}
	if err := a.saveState(next); err != nil {
		return rollback(err)
	}
	if err := os.Remove(pendingPath); err != nil {
		return rollback(err)
	}
	a.state = next
	a.pruneBackups()
	return nil
}

func (a *App) mutate(ctx context.Context, action Action) error {
	if _, err := os.Stat(filepath.Join(a.options.Root, "pending.json")); err == nil {
		return errors.New("上次事务尚未恢复完成，请重启配置管理服务以恢复备份")
	}
	if action.Revision != a.state.Revision {
		return errors.New("配置已被更新，请刷新后重试")
	}
	if action.Op != "adopt" && a.drift() {
		return errors.New("设备配置已被其他工具修改，请先点击“采用设备当前配置”")
	}
	next := clone(a.state)
	sourceIndex := func() int {
		for i, item := range next.Sources {
			if item.ID == action.ID {
				return i
			}
		}
		return -1
	}
	switch action.Op {
	case "save-source":
		if action.Mode != "pool" && action.Mode != "full" || strings.TrimSpace(action.Name) == "" || strings.ContainsAny(action.Name, ",\r\n") || len(action.Name) > 100 {
			return errors.New("请填写合法订阅名称和导入方式")
		}
		if action.IntervalHours < 1 || action.IntervalHours > 720 {
			return errors.New("更新间隔需要在 1 到 720 小时之间")
		}
		index := sourceIndex()
		source := Source{ID: action.ID, Name: action.Name, URL: action.URL, Mode: action.Mode, Enabled: action.Enabled, UserAgent: action.UserAgent, IntervalHours: action.IntervalHours}
		if source.ID == "" {
			source.ID = newID()
		} else if index < 0 {
			return errors.New("订阅不存在")
		}
		for _, other := range next.Sources {
			if other.ID != source.ID && other.Name == source.Name {
				return errors.New("订阅名称不能重复")
			}
		}
		if index >= 0 && next.Active == source.ID && source.Mode != "full" {
			return errors.New("请先切换到其他完整配置，再修改导入方式")
		}
		if source.URL != "" {
			if err := fetchSource(ctx, &source, a.options.Curl); err != nil {
				return err
			}
		} else if source.Mode == "full" {
			if _, err := parseYAML(action.Content); err != nil {
				return err
			}
			source.Config = action.Content
		} else {
			return errors.New("节点池订阅需要 HTTP/HTTPS 链接；本地节点请使用手动添加")
		}
		if index >= 0 {
			next.Sources[index] = source
		} else {
			next.Sources = append(next.Sources, source)
		}
	case "save-source-content":
		i := sourceIndex()
		if i < 0 || next.Sources[i].Mode != "full" {
			return errors.New("完整配置不存在")
		}
		if _, err := parseYAML(action.Content); err != nil {
			return err
		}
		next.Sources[i].Config = action.Content
	case "refresh-source":
		i := sourceIndex()
		if i < 0 || next.Sources[i].URL == "" {
			return errors.New("该配置没有可更新的订阅链接")
		}
		if err := fetchSource(ctx, &next.Sources[i], a.options.Curl); err != nil {
			return err
		}
	case "toggle-source", "delete-source":
		i := sourceIndex()
		if i < 0 {
			return errors.New("订阅不存在")
		}
		if action.Op == "toggle-source" {
			next.Sources[i].Enabled = action.Enabled
		} else {
			if next.Active == action.ID {
				return errors.New("请先切换完整配置，再删除当前配置")
			}
			next.Sources = append(next.Sources[:i], next.Sources[i+1:]...)
		}
	case "activate":
		next.Active, next.Disabled = action.ID, nil
	case "mode":
		next.Mode = action.Mode
	case "save-manual":
		nodes, err := parseNodes(action.Content)
		if err != nil {
			return err
		}
		if action.ID != "" {
			found := false
			if len(nodes) != 1 {
				return errors.New("编辑单个节点时只能提交一个节点")
			}
			for i := range next.Manual {
				if next.Manual[i].ID == action.ID {
					next.Manual[i].Node, found = nodes[0], true
				}
			}
			if !found {
				return errors.New("手动节点不存在")
			}
		} else {
			for _, node := range nodes {
				next.Manual = append(next.Manual, ManualNode{newID(), true, node})
			}
		}
	case "toggle-manual", "delete-manual":
		found := false
		for i, node := range next.Manual {
			if node.ID != action.ID {
				continue
			}
			found = true
			if action.Op == "toggle-manual" {
				next.Manual[i].Enabled = action.Enabled
			} else {
				next.Manual = append(next.Manual[:i], next.Manual[i+1:]...)
			}
			break
		}
		if !found {
			return errors.New("手动节点不存在")
		}
	case "save-chain":
		chain := Chain{action.ID, action.Name, action.Via, action.Exit, action.Enabled}
		if chain.ID == "" {
			chain.ID = newID()
			next.Chains = append(next.Chains, chain)
		} else {
			found := false
			for i := range next.Chains {
				if next.Chains[i].ID == chain.ID {
					next.Chains[i], found = chain, true
				}
			}
			if !found {
				return errors.New("链式代理不存在")
			}
		}
	case "delete-chain", "toggle-chain":
		found := false
		for i, chain := range next.Chains {
			if chain.ID != action.ID {
				continue
			}
			found = true
			if action.Op == "toggle-chain" {
				next.Chains[i].Enabled = action.Enabled
			} else {
				next.Chains = append(next.Chains[:i], next.Chains[i+1:]...)
			}
			break
		}
		if !found {
			return errors.New("链式代理不存在")
		}
	case "nodes":
		next.Disabled = action.Names
	case "routes":
		next.Routes = action.Rules
	case "dns":
		next.DNS = action.Values
	case "advanced":
		next.Advanced = action.Values
	case "resources":
		next.Resources = action.Values
	case "save-base":
		if _, err := parseYAML(action.Content); err != nil {
			return err
		}
		next.Base, next.Active = action.Content, "current"
	case "restore":
		if filepath.Base(action.ID) != action.ID || !strings.HasSuffix(action.ID, ".json") {
			return errors.New("备份标识无效")
		}
		data, err := os.ReadFile(filepath.Join(a.options.Root, "backups", action.ID))
		if err != nil {
			return errors.New("备份不存在")
		}
		var old snapshot
		if json.Unmarshal(data, &old) != nil {
			return errors.New("备份内容损坏")
		}
		next = old.State
		// Endpoints and credentials always come from this device, not a backup.
		next.Pins, next.DNSListen = a.state.Pins, a.state.DNSListen
	case "adopt":
		data, err := os.ReadFile(a.options.Config)
		if err != nil {
			return err
		}
		cfg, err := parseYAML(string(data))
		if err != nil {
			return err
		}
		if stringOf(cfg["secret"]) != a.secret {
			return errors.New("设备控制器密码已改变，请重启管理服务后重新连接")
		}
		// The current config already contains overlays. Clear them to avoid duplication.
		next = State{Revision: a.state.Revision + 1, Base: string(data), Active: "current", Mode: "full", Pins: Object{}, ConfigHash: hash(data)}
		for _, key := range pinnedKeys {
			if value, ok := cfg[key]; ok {
				next.Pins[key] = value
			}
		}
		next.DNSListen = objectOf(cfg["dns"])["listen"]
		if _, err := a.backup(snapshot{a.state, string(data)}); err != nil {
			return err
		}
		if err := a.saveState(next); err != nil {
			return err
		}
		a.state = next
		return nil
	case "reload":
		return a.reload(ctx)
	case "restart":
		if a.options.Service == "" {
			return errors.New("未配置 F50 服务脚本")
		}
		ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
		defer cancel()
		// stop may exit nonzero if the process was already stopped.
		_ = exec.CommandContext(ctx, "/system/bin/sh", a.options.Service, "stop").Run()
		if err := exec.CommandContext(ctx, "/system/bin/sh", a.options.Service, "start").Run(); err != nil {
			return errors.New("服务未能启动，请检查猫猫内核日志")
		}
		return nil
	default:
		return errors.New("未知操作")
	}
	return a.commit(ctx, next)
}

func (a *App) scheduled(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshDue(ctx)
		}
	}
}

func (a *App) refreshDue(ctx context.Context) {
	a.mu.Lock()
	ids := []string{}
	for _, source := range a.state.Sources {
		ids = append(ids, source.ID)
	}
	a.mu.Unlock()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		if a.drift() {
			a.mu.Unlock()
			return
		}
		i := -1
		for index, source := range a.state.Sources {
			if source.ID == id && source.Enabled && source.URL != "" && source.IntervalHours > 0 && time.Now().Unix()-source.AttemptAt >= int64(source.IntervalHours)*3600 {
				i = index
				break
			}
		}
		if i < 0 {
			a.mu.Unlock()
			continue
		}
		next := clone(a.state)
		revision := a.state.Revision
		a.mu.Unlock()
		// Slow subscription servers must not block the panel or overwrite newer edits.
		err := fetchSource(ctx, &next.Sources[i], a.options.Curl)
		a.mu.Lock()
		if a.state.Revision != revision || a.drift() {
			a.mu.Unlock()
			continue
		}
		if err == nil {
			err = a.commit(ctx, next)
		}
		if err != nil {
			next = clone(a.state)
			next.Sources[i].AttemptAt = time.Now().Unix()
			next.Sources[i].LastError = err.Error()
			next.Revision++
			if a.saveState(next) == nil {
				a.state = next
			}
		}
		a.mu.Unlock()
	}
}
