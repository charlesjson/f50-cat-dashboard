package main

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func hostOnly(host string) string {
	if name, _, err := net.SplitHostPort(host); err == nil {
		return name
	}
	return host
}

func (a *App) handler(devOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote := net.ParseIP(hostOnly(r.RemoteAddr))
		if remote == nil || (!remote.IsPrivate() && !remote.IsLoopback() && !remote.IsLinkLocalUnicast()) {
			writeJSON(w, http.StatusForbidden, Object{"error": "仅允许局域网访问"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			allowed := err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Hostname(), hostOnly(r.Host)) && (u.Port() == "7788" || u.Port() == "7790")
			if !allowed && (devOrigin == "" || origin != devOrigin) {
				writeJSON(w, http.StatusForbidden, Object{"error": "请从设备面板打开配置管理"})
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.secret)) != 1 {
			writeJSON(w, http.StatusUnauthorized, Object{"error": "控制器密码不正确，请在面板连接设置中填写"})
			return
		}
		if (r.Method != http.MethodGet && r.Method != http.MethodPost) || !strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, Object{"error": "接口不存在"})
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/state":
			view, err := a.view()
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, Object{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, view)
		case r.Method == http.MethodGet && r.URL.Path == "/api/preview":
			data, _, err := compile(a.state)
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, Object{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, Object{"content": string(data)})
		case r.Method == http.MethodGet && r.URL.Path == "/api/source":
			id := r.URL.Query().Get("id")
			if id == "current" {
				writeJSON(w, http.StatusOK, Object{"content": a.state.Base})
				return
			}
			for _, source := range a.state.Sources {
				if source.ID == id && source.Mode == "full" {
					writeJSON(w, http.StatusOK, Object{"content": source.Config})
					return
				}
			}
			writeJSON(w, http.StatusNotFound, Object{"error": "配置不存在"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/backups":
			entries, _ := os.ReadDir(filepath.Join(a.options.Root, "backups"))
			list := []Object{}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				if info, err := entry.Info(); err == nil {
					list = append(list, Object{"id": entry.Name(), "createdAt": info.ModTime().Unix(), "bytes": info.Size()})
				}
			}
			sort.Slice(list, func(i, j int) bool { return stringOf(list[i]["id"]) > stringOf(list[j]["id"]) })
			writeJSON(w, http.StatusOK, Object{"items": list})
		case r.Method == http.MethodPost && (r.URL.Path == "/api/mutate" || r.URL.Path == "/api/validate"):
			r.Body = http.MaxBytesReader(w, r.Body, maxConfig+65536)
			var action Action
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&action); err != nil {
				writeJSON(w, http.StatusBadRequest, Object{"error": "请求格式错误或超过大小限制"})
				return
			}
			if r.URL.Path == "/api/validate" {
				next := clone(a.state)
				next.Base, next.Active = action.Content, "current"
				data, _, err := compile(next)
				if err == nil {
					err = a.validate(r.Context(), data)
				}
				if err != nil {
					writeJSON(w, http.StatusUnprocessableEntity, Object{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, Object{"ok": true})
				return
			}
			if err := a.mutate(r.Context(), action); err != nil {
				status := http.StatusUnprocessableEntity
				if action.Revision != a.state.Revision || a.drift() {
					status = http.StatusConflict
				}
				writeJSON(w, status, Object{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, Object{"ok": true, "revision": a.state.Revision})
		default:
			writeJSON(w, http.StatusNotFound, Object{"error": "接口不存在"})
		}
	})
}

func (a *App) view() (Object, error) {
	data, nodes, err := compile(a.state)
	if err != nil {
		return nil, err
	}
	cfg, err := parseYAML(string(data))
	if err != nil {
		return nil, err
	}
	sources := []Object{}
	for _, source := range a.state.Sources {
		sources = append(sources, Object{"id": source.ID, "name": source.Name, "url": source.URL, "mode": source.Mode, "enabled": source.Enabled, "userAgent": source.UserAgent, "intervalHours": source.IntervalHours, "updatedAt": source.UpdatedAt, "lastError": source.LastError, "nodeCount": len(source.Nodes)})
	}
	groups := []string{}
	for _, group := range objects(cfg["proxy-groups"]) {
		groups = append(groups, stringOf(group["name"]))
	}
	advanced := Object{}
	dns := clone(objectOf(cfg["dns"]))
	dns["hosts"] = objectOf(cfg["hosts"])
	for key := range advancedKeys {
		if value, ok := cfg[key]; ok {
			advanced[key] = value
		}
	}
	return Object{
		"version": version, "revision": a.state.Revision, "mode": a.state.Mode, "active": a.state.Active,
		"sources": sources, "manual": nonNil(a.state.Manual), "chains": nonNil(a.state.Chains), "routes": nonNil(a.state.Routes),
		"nodes": nonNil(nodes), "groups": groups, "disabled": nonNil(a.state.Disabled), "dns": dns,
		"advanced": advanced, "resources": Object{"geox-url": objectOf(cfg["geox-url"]), "geo-auto-update": cfg["geo-auto-update"] == true, "geo-update-interval": cfg["geo-update-interval"]},
		"drift": a.drift(), "ruleCount": len(stringsOf(cfg["rules"])), "transport": "TProxy",
	}, nil
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
