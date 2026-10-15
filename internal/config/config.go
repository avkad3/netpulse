// Package config loads NetPulse settings and probe targets from a JSON file.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Probe types supported by NetPulse.
const (
	TypeDNS  = "dns"
	TypeHTTP = "http"
	TypeTCP  = "tcp"
)

// Duration is a time.Duration that unmarshals from strings such as "15s".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"15s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Target describes one thing to probe.
type Target struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Host     string   `json:"host,omitempty"`    // dns: name to resolve
	Server   string   `json:"server,omitempty"`  // dns: resolver "ip:port" (empty = system)
	URL      string   `json:"url,omitempty"`     // http
	Address  string   `json:"address,omitempty"` // tcp: "host:port"
	Interval Duration `json:"interval"`
	Timeout  Duration `json:"timeout"`
}

// Config is the whole NetPulse configuration.
type Config struct {
	Listen    string   `json:"listen"`
	DBPath    string   `json:"db_path"`
	Retention Duration `json:"retention"`
	Workers   int      `json:"workers"`
	Targets   []Target `json:"targets"`
}

// Default returns a configuration that probes a few well-known public services.
func Default() Config {
	s := func(d time.Duration) Duration { return Duration{d} }
	return Config{
		Listen:    ":8080",
		DBPath:    "netpulse.db",
		Retention: s(7 * 24 * time.Hour),
		Workers:   8,
		Targets: []Target{
			{Name: "cloudflare-dns", Type: TypeDNS, Host: "example.com", Server: "1.1.1.1:53", Interval: s(15 * time.Second)},
			{Name: "google-dns", Type: TypeDNS, Host: "example.com", Server: "8.8.8.8:53", Interval: s(15 * time.Second)},
			{Name: "google-http", Type: TypeHTTP, URL: "https://www.google.com", Interval: s(30 * time.Second)},
			{Name: "github-http", Type: TypeHTTP, URL: "https://github.com", Interval: s(30 * time.Second)},
			{Name: "cloudflare-tcp", Type: TypeTCP, Address: "1.1.1.1:443", Interval: s(15 * time.Second)},
		},
	}
}

// Load reads a JSON config file. Missing fields fall back to Default().
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	// Decode over the defaults, but replace (not merge) the target list.
	cfg.Targets = nil
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, cfg.Validate()
}

// Validate fills per-target defaults and checks the configuration.
func (c *Config) Validate() error {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if len(c.Targets) == 0 {
		return fmt.Errorf("no targets configured")
	}
	seen := map[string]bool{}
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.Name == "" {
			return fmt.Errorf("target %d: name is required", i)
		}
		if seen[t.Name] {
			return fmt.Errorf("target %q: duplicate name", t.Name)
		}
		seen[t.Name] = true
		if t.Interval.Duration <= 0 {
			t.Interval.Duration = 30 * time.Second
		}
		if t.Timeout.Duration <= 0 {
			t.Timeout.Duration = 5 * time.Second
		}
		if t.Timeout.Duration > t.Interval.Duration {
			return fmt.Errorf("target %q: timeout must not exceed interval", t.Name)
		}
		switch t.Type {
		case TypeDNS:
			if t.Host == "" {
				return fmt.Errorf("target %q: dns probe needs host", t.Name)
			}
		case TypeHTTP:
			u, err := url.Parse(t.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("target %q: http probe needs an http(s) url", t.Name)
			}
		case TypeTCP:
			if !strings.Contains(t.Address, ":") {
				return fmt.Errorf("target %q: tcp probe needs address host:port", t.Name)
			}
		default:
			return fmt.Errorf("target %q: unknown type %q (want dns, http or tcp)", t.Name, t.Type)
		}
	}
	return nil
}
