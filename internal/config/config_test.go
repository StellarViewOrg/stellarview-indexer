package config

import (
	"os"
	"testing"
)

func TestLoad_RPCEndpointOptional(t *testing.T) {
	os.Unsetenv("RPC_ENDPOINT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RPCEndpoint != "" {
		t.Errorf("expected empty RPCEndpoint, got '%s'", cfg.RPCEndpoint)
	}
}

func TestLoad_Defaults(t *testing.T) {
	os.Setenv("RPC_ENDPOINT", "https://rpc.example.com")
	defer os.Unsetenv("RPC_ENDPOINT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Network != "public" {
		t.Errorf("expected network 'public', got '%s'", cfg.Network)
	}
	if cfg.BatchSize != 100 {
		t.Errorf("expected batch size 100, got %d", cfg.BatchSize)
	}
	if cfg.WorkerCount != 8 {
		t.Errorf("expected worker count 8, got %d", cfg.WorkerCount)
	}
	if cfg.MetricsAddr != "" {
		t.Errorf("expected metrics server disabled by default, got MetricsAddr=%q", cfg.MetricsAddr)
	}
}

func TestLoad_MetricsAddrOptIn(t *testing.T) {
	os.Setenv("RPC_ENDPOINT", "https://rpc.example.com")
	os.Setenv("METRICS_ADDR", ":9090")
	defer func() {
		os.Unsetenv("RPC_ENDPOINT")
		os.Unsetenv("METRICS_ADDR")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MetricsAddr != ":9090" {
		t.Errorf("expected MetricsAddr ':9090', got '%s'", cfg.MetricsAddr)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	os.Setenv("RPC_ENDPOINT", "https://rpc.example.com")
	os.Setenv("NETWORK", "testnet")
	os.Setenv("BATCH_SIZE", "200")
	defer func() {
		os.Unsetenv("RPC_ENDPOINT")
		os.Unsetenv("NETWORK")
		os.Unsetenv("BATCH_SIZE")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Network != "testnet" {
		t.Errorf("expected network 'testnet', got '%s'", cfg.Network)
	}
	if cfg.BatchSize != 200 {
		t.Errorf("expected batch size 200, got %d", cfg.BatchSize)
	}
}
