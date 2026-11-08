package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func decodeBase64(value string) ([]byte, error) {
	value = strings.Join(strings.Fields(value), "")
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("Base64 格式错误")
}

func parseNodes(content string) ([]Object, error) {
	content = strings.TrimSpace(strings.TrimPrefix(content, "\ufeff"))
	if content == "" || len(content) > maxConfig {
		return nil, errors.New("节点内容为空或超过 5 MiB")
	}
	if nodes, ok := yamlNodes(content); ok {
		return checkNodes(nodes)
	}
	if !strings.Contains(content, "://") {
		if decoded, err := decodeBase64(content); err == nil {
			content = string(decoded)
		}
	}
	var nodes []Object
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		node, err := parseURI(line)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个节点: %w", len(nodes)+1, err)
		}
		nodes = append(nodes, node)
	}
	return checkNodes(nodes)
}

func yamlNodes(content string) ([]Object, bool) {
	var value any
	if yaml.Unmarshal([]byte(content), &value) != nil {
		return nil, false
	}
	if obj, ok := value.(map[string]any); ok {
		if list, ok := obj["proxies"]; ok {
			return objects(list), true
		}
		if obj["server"] != nil && obj["type"] != nil {
			return []Object{obj}, true
		}
	}
	if list, ok := value.([]any); ok {
		return objects(list), true
	}
	return nil, false
}

func checkNodes(nodes []Object) ([]Object, error) {
	if len(nodes) == 0 || len(nodes) > 10000 {
		return nil, errors.New("没有识别到节点（支持 Clash YAML、分享链接和 Base64 订阅）")
	}
	names := map[string]bool{}
	for i, node := range nodes {
		name := stringOf(node["name"])
		if name == "" {
			name = fmt.Sprintf("节点 %d", i+1)
			node["name"] = name
		}
		if names[name] || strings.ContainsAny(name, ",\r\n") {
			return nil, errors.New("节点名称重复或含不支持的字符")
		}
		names[name] = true
		if node["type"] == nil || node["server"] == nil || node["port"] == nil {
			return nil, fmt.Errorf("节点 %s 缺少 type/server/port", name)
		}
	}
	return nodes, nil
}

func parseURI(line string) (Object, error) {
	if strings.HasPrefix(line, "vmess://") {
		data, err := decodeBase64(strings.TrimPrefix(line, "vmess://"))
		if err != nil {
			return nil, err
		}
		var v Object
		if json.Unmarshal(data, &v) != nil {
			return nil, errors.New("VMess JSON 格式错误")
		}
		port, err := strconv.Atoi(stringOf(v["port"]))
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("VMess 端口无效")
		}
		alterID, _ := strconv.Atoi(stringOf(v["aid"]))
		node := Object{"name": stringOf(v["ps"]), "type": "vmess", "server": v["add"], "port": port, "uuid": v["id"], "alterId": alterID, "cipher": "auto", "udp": true}
		if cipher := stringOf(v["scy"]); cipher != "" {
			node["cipher"] = cipher
		}
		query := url.Values{"type": {stringOf(v["net"])}, "path": {stringOf(v["path"])}, "host": {stringOf(v["host"])}, "sni": {stringOf(v["sni"])}, "fp": {stringOf(v["fp"])}, "alpn": {stringOf(v["alpn"])}}
		if stringOf(v["tls"]) == "tls" {
			query.Set("security", "tls")
		}
		transport(node, query)
		return node, nil
	}
	if strings.HasPrefix(line, "ss://") {
		return parseSS(line)
	}
	u, err := url.Parse(line)
	if err != nil || u.Hostname() == "" || u.User == nil {
		return nil, errors.New("分享链接格式错误")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("分享链接端口无效")
	}
	name := u.Fragment
	if name == "" {
		name = u.Scheme + "-" + u.Hostname()
	}
	node := Object{"name": name, "type": u.Scheme, "server": u.Hostname(), "port": port, "udp": true}
	query := u.Query()
	switch u.Scheme {
	case "vless":
		node["uuid"] = u.User.Username()
		if flow := query.Get("flow"); flow != "" {
			node["flow"] = flow
		}
	case "trojan":
		node["password"] = u.User.Username()
		if query.Get("security") == "" {
			query.Set("security", "tls")
		}
	case "hysteria2", "hy2":
		node["type"] = "hysteria2"
		node["password"] = u.User.Username()
		if pass, ok := u.User.Password(); ok {
			node["password"] = u.User.Username() + ":" + pass
		}
		query.Set("security", "tls")
		if obfs := query.Get("obfs"); obfs != "" {
			node["obfs"], node["obfs-password"] = obfs, query.Get("obfs-password")
		}
	case "tuic":
		node["uuid"] = u.User.Username()
		node["password"], _ = u.User.Password()
		query.Set("security", "tls")
		if cc := query.Get("congestion_control"); cc != "" {
			node["congestion-controller"] = cc
		}
	default:
		return nil, fmt.Errorf("不支持 %s 分享链接，请使用 Clash YAML", u.Scheme)
	}
	transport(node, query)
	return node, nil
}

func transport(node Object, q url.Values) {
	if security := q.Get("security"); security == "tls" || security == "reality" {
		node["tls"] = true
		if security == "reality" {
			node["reality-opts"] = Object{"public-key": q.Get("pbk"), "short-id": q.Get("sid")}
		}
	}
	if sni := q.Get("sni"); sni != "" {
		key := "servername"
		if node["type"] == "trojan" || node["type"] == "hysteria2" || node["type"] == "tuic" {
			key = "sni"
		}
		node[key] = sni
	}
	if fp := q.Get("fp"); fp != "" {
		node["client-fingerprint"] = fp
	}
	if alpn := q.Get("alpn"); alpn != "" {
		node["alpn"] = strings.Split(alpn, ",")
	}
	if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" {
		node["skip-cert-verify"] = true
	}
	network := q.Get("type")
	if network == "" || network == "tcp" {
		return
	}
	node["network"] = network
	switch network {
	case "ws", "httpupgrade":
		opts := Object{"path": q.Get("path")}
		if host := q.Get("host"); host != "" {
			opts["headers"] = Object{"Host": host}
		}
		node["ws-opts"] = opts
	case "grpc":
		service := q.Get("serviceName")
		if service == "" {
			service = q.Get("path")
		}
		node["grpc-opts"] = Object{"grpc-service-name": service}
	case "h2":
		node["h2-opts"] = Object{"path": q.Get("path"), "host": strings.Split(q.Get("host"), ",")}
	}
}

func parseSS(line string) (Object, error) {
	raw := strings.TrimPrefix(line, "ss://")
	if !strings.Contains(strings.SplitN(raw, "#", 2)[0], "@") {
		parts := strings.SplitN(raw, "#", 2)
		decoded, err := decodeBase64(parts[0])
		if err != nil {
			return nil, err
		}
		raw = string(decoded)
		if len(parts) == 2 {
			raw += "#" + parts[1]
		}
	}
	u, err := url.Parse("ss://" + raw)
	if err != nil || u.User == nil || u.Hostname() == "" {
		return nil, errors.New("Shadowsocks 链接格式错误")
	}
	method := u.User.Username()
	password, ok := u.User.Password()
	if !ok {
		decoded, err := decodeBase64(method)
		if err != nil {
			return nil, err
		}
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return nil, errors.New("Shadowsocks 认证格式错误")
		}
		method, password = parts[0], parts[1]
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("Shadowsocks 端口无效")
	}
	name := u.Fragment
	if name == "" {
		name = "ss-" + u.Hostname()
	}
	node := Object{"name": name, "type": "ss", "server": u.Hostname(), "port": port, "cipher": method, "password": password, "udp": true}
	if plugin := u.Query().Get("plugin"); plugin != "" {
		parts := strings.Split(plugin, ";")
		node["plugin"] = parts[0]
		opts := Object{}
		for _, item := range parts[1:] {
			kv := strings.SplitN(item, "=", 2)
			if len(kv) == 2 {
				opts[kv[0]] = kv[1]
			} else {
				opts[kv[0]] = true
			}
		}
		node["plugin-opts"] = opts
	}
	return node, nil
}

func validSourceURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("订阅需要合法的 HTTP/HTTPS 链接")
	}
	return nil
}

type cappedWriter struct {
	data []byte
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if len(w.data)+len(p) > maxConfig {
		return 0, errors.New("订阅超过 5 MiB")
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func fetchSource(ctx context.Context, source *Source, curl string) error {
	if err := validSourceURL(source.URL); err != nil {
		return err
	}
	if strings.ContainsAny(source.UserAgent, "\r\n\x00") || len(source.UserAgent) > 300 {
		return errors.New("User-Agent 无效")
	}
	ua := source.UserAgent
	if ua == "" {
		ua = "clash.meta/f50-cat-dashboard"
	}
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	var data []byte
	if curl != "" {
		// URL goes through stdin, not the process command line or a shell.
		quoted := strconv.Quote(source.URL)
		cmd := exec.CommandContext(ctx, curl, "--config", "-", "--noproxy", "*", "--fail", "--silent", "--location", "--max-redirs", "4", "--proto", "=http,https", "--proto-redir", "=http,https", "--connect-timeout", "8", "--max-time", "45", "--max-filesize", strconv.Itoa(maxConfig), "--user-agent", ua)
		cmd.Stdin = strings.NewReader("url = " + quoted + "\n")
		out := &cappedWriter{}
		cmd.Stdout = out
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("订阅下载失败（curl %d），请检查链接、网络及 UA；当前缓存已保留", exit.ExitCode())
			}
			return errors.New("无法启动订阅下载程序，请检查设备 curl 路径")
		}
		data = out.data
	} else {
		client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 || validSourceURL(req.URL.String()) != nil {
				return errors.New("订阅重定向无效")
			}
			return nil
		}}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := client.Do(req)
		if err != nil {
			return errors.New("订阅下载失败，请检查链接和网络；当前缓存已保留")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("订阅返回 HTTP %d；当前缓存已保留", resp.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxConfig+1))
		if err != nil || len(data) > maxConfig {
			return errors.New("订阅读取失败或超过 5 MiB")
		}
	}
	if source.Mode == "full" {
		cfg, err := parseYAML(string(data))
		if err != nil {
			return err
		}
		if cfg["proxies"] == nil && cfg["proxy-providers"] == nil {
			return errors.New("完整配置必须包含 proxies 或 proxy-providers")
		}
		source.Config, source.Nodes = string(data), nil
	} else {
		nodes, err := parseNodes(string(data))
		if err != nil {
			return err
		}
		source.Nodes, source.Config = nodes, ""
	}
	source.UpdatedAt, source.AttemptAt, source.LastError = time.Now().Unix(), time.Now().Unix(), ""
	return nil
}
