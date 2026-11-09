package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxConfig = 5 << 20

type Object = map[string]any

type Source struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Mode          string   `json:"mode"`
	Enabled       bool     `json:"enabled"`
	UserAgent     string   `json:"userAgent"`
	IntervalHours int      `json:"intervalHours"`
	UpdatedAt     int64    `json:"updatedAt"`
	AttemptAt     int64    `json:"attemptAt"`
	LastError     string   `json:"lastError"`
	Nodes         []Object `json:"nodes,omitempty"`
	Config        string   `json:"config,omitempty"`
}

type ManualNode struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Node    Object `json:"node"`
}

type Chain struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Via     string `json:"via"`
	Exit    string `json:"exit"`
	Enabled bool   `json:"enabled"`
}

type Route struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Target    string `json:"target"`
	NoResolve bool   `json:"noResolve"`
}

type GroupEdit struct {
	ID          string   `json:"id"`
	Original    string   `json:"original,omitempty"`
	Config      Object   `json:"config,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Deleted     bool     `json:"deleted,omitempty"`
	Replacement string   `json:"replacement,omitempty"`
}

type GroupView struct {
	ID         string   `json:"id"`
	Config     Object   `json:"config"`
	Modified   bool     `json:"modified"`
	Custom     bool     `json:"custom"`
	References []string `json:"references"`
}

type State struct {
	Revision   int64                  `json:"revision"`
	Mode       string                 `json:"mode"`
	Active     string                 `json:"active"`
	Base       string                 `json:"base"`
	Pins       Object                 `json:"pins"`
	DNSListen  any                    `json:"dnsListen,omitempty"`
	Sources    []Source               `json:"sources"`
	Manual     []ManualNode           `json:"manual"`
	Chains     []Chain                `json:"chains"`
	Routes     []Route                `json:"routes"`
	Disabled   []string               `json:"disabled"`
	DNS        Object                 `json:"dns"`
	Advanced   Object                 `json:"advanced"`
	Resources  Object                 `json:"resources"`
	ConfigHash string                 `json:"configHash"`
	GroupEdits map[string][]GroupEdit `json:"groupEdits,omitempty"`
}

type Action struct {
	Op            string   `json:"op"`
	Revision      int64    `json:"revision"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Mode          string   `json:"mode"`
	Content       string   `json:"content"`
	Enabled       bool     `json:"enabled"`
	UserAgent     string   `json:"userAgent"`
	IntervalHours int      `json:"intervalHours"`
	Via           string   `json:"via"`
	Exit          string   `json:"exit"`
	Names         []string `json:"names"`
	Rules         []Route  `json:"rules"`
	Values        Object   `json:"values"`
	Group         Object   `json:"group"`
	Replacement   string   `json:"replacement"`
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func clone[T any](v T) T {
	b, _ := json.Marshal(v)
	var copy T
	_ = json.Unmarshal(b, &copy)
	return copy
}

func parseYAML(data string) (Object, error) {
	if len(data) == 0 || len(data) > maxConfig {
		return nil, errors.New("配置为空或超过 5 MiB")
	}
	var obj Object
	d := yaml.NewDecoder(strings.NewReader(data))
	if err := d.Decode(&obj); err != nil {
		return nil, fmt.Errorf("YAML 格式错误: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("只支持单份 YAML 文档")
	}
	if obj == nil {
		return nil, errors.New("配置必须是 YAML 对象")
	}
	return obj, nil
}

func marshalYAML(obj any) ([]byte, error) {
	var buf bytes.Buffer
	e := yaml.NewEncoder(&buf)
	e.SetIndent(2)
	err := e.Encode(obj)
	_ = e.Close()
	return buf.Bytes(), err
}

func objects(v any) []Object {
	var result []Object
	switch list := v.(type) {
	case []any:
		for _, item := range list {
			if obj, ok := item.(map[string]any); ok {
				result = append(result, obj)
			}
		}
	case []Object:
		result = list
	}
	return result
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func stringsOf(v any) []string {
	var result []string
	switch list := v.(type) {
	case []any:
		for _, item := range list {
			result = append(result, stringOf(item))
		}
	case []string:
		result = list
	}
	return result
}

func objectOf(v any) Object {
	if obj, ok := v.(map[string]any); ok {
		return obj
	}
	return Object{}
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".f50-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func jsonBytes(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
