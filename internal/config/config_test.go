package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tg := range cfg.Targets {
		if tg.Timeout.Duration != 5*time.Second {
			t.Errorf("%s: default timeout not applied", tg.Name)
		}
	}
}

func TestLoadReplacesTargetsAndParsesDurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(path, []byte(`{
		"listen": ":9090",
		"targets": [{"name": "a", "type": "tcp", "address": "1.1.1.1:443", "interval": "10s", "timeout": "2s"}]
	}`), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9090" || len(cfg.Targets) != 1 || cfg.DBPath != "netpulse.db" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Targets[0].Interval.Duration != 10*time.Second {
		t.Fatalf("interval = %v", cfg.Targets[0].Interval)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]Target{
		"no name":          {Type: TypeTCP, Address: "a:1"},
		"bad type":         {Name: "x", Type: "icmp"},
		"dns without host": {Name: "x", Type: TypeDNS},
		"bad url":          {Name: "x", Type: TypeHTTP, URL: "ftp://x"},
		"tcp without port": {Name: "x", Type: TypeTCP, Address: "host"},
		"timeout > interval": {Name: "x", Type: TypeTCP, Address: "a:1",
			Interval: Duration{time.Second}, Timeout: Duration{2 * time.Second}},
	}
	for name, tg := range cases {
		cfg := Config{Targets: []Target{tg}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	dup := Config{Targets: []Target{
		{Name: "x", Type: TypeTCP, Address: "a:1"}, {Name: "x", Type: TypeTCP, Address: "b:1"},
	}}
	if err := dup.Validate(); err == nil {
		t.Error("duplicate names: expected error")
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(path, []byte(`{"listn": ":1", "targets": []}`), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for typo'd field")
	}
}

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) < 5 {
		t.Fatalf("example has %d targets", len(cfg.Targets))
	}
}
