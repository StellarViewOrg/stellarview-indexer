package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/stellar/go-stellar-sdk/network"
)

type Config struct {
	DatabaseURL  string
	RedisURL     string
	RPCEndpoint  string
	DataLakePath string
	Network      string // "public", "testnet", "futurenet"
	BatchSize    int
	WorkerCount  int
	MetricsAddr  string // listen address for /metrics and /healthz; disabled when empty

	VerifyAPIAddr          string  // listen address for the contract verification API
	VerifyBuilderImage     string  // Docker image used for sandboxed reproducible builds
	VerifyWorkspaceDir     string  // scratch dir for build workspaces; OS temp when empty
	VerifyMaxArchiveMB     int     // max compressed upload size in MB
	VerifyMaxExtractedMB   int     // max uncompressed source size in MB
	VerifyRateRPS          float64 // verification submissions per second per IP
	VerifyRateBurst        int     // per-IP burst allowance
	VerifyQueueSize        int     // max queued verification jobs
	VerifyBuildConcurrency int     // parallel sandboxed builds
	VerifyBuildTimeoutMin  int     // per-build timeout in minutes
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:  getEnv("DATABASE_URL", "postgresql://explorer:explorer_dev@localhost:54320/stellar_explorer?sslmode=disable"),
		RedisURL:     getEnv("REDIS_URL", "redis://localhost:63790"),
		RPCEndpoint:  getEnv("RPC_ENDPOINT", ""),
		DataLakePath: getEnv("DATA_LAKE_PATH", "s3://aws-public-blockchain/v1.1/stellar/ledgers/pubnet"),
		Network:      getEnv("NETWORK", "public"),
		BatchSize:    getEnvInt("BATCH_SIZE", 100),
		WorkerCount:  getEnvInt("WORKER_COUNT", 8),
		MetricsAddr:  getEnv("METRICS_ADDR", ""),

		VerifyAPIAddr:          getEnv("VERIFY_API_ADDR", ":8080"),
		VerifyBuilderImage:     getEnv("VERIFY_BUILDER_IMAGE", "stellarview/soroban-builder:latest"),
		VerifyWorkspaceDir:     getEnv("VERIFY_WORKSPACE_DIR", ""),
		VerifyMaxArchiveMB:     getEnvInt("VERIFY_MAX_ARCHIVE_MB", 20),
		VerifyMaxExtractedMB:   getEnvInt("VERIFY_MAX_EXTRACTED_MB", 100),
		VerifyRateRPS:          getEnvFloat("VERIFY_RATE_RPS", 1),
		VerifyRateBurst:        getEnvInt("VERIFY_RATE_BURST", 5),
		VerifyQueueSize:        getEnvInt("VERIFY_QUEUE_SIZE", 16),
		VerifyBuildConcurrency: getEnvInt("VERIFY_BUILD_CONCURRENCY", 2),
		VerifyBuildTimeoutMin:  getEnvInt("VERIFY_BUILD_TIMEOUT_MIN", 20),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	switch c.Network {
	case "public", "testnet", "futurenet":
		// valid
	default:
		return fmt.Errorf("invalid NETWORK %q: must be one of public, testnet, futurenet", c.Network)
	}

	if c.WorkerCount <= 0 {
		return fmt.Errorf("invalid WORKER_COUNT %d: must be > 0", c.WorkerCount)
	}

	if c.BatchSize <= 0 {
		return fmt.Errorf("invalid BATCH_SIZE %d: must be > 0", c.BatchSize)
	}

	return nil
}

// NetworkPassphrase returns the Stellar network passphrase for the configured network.
func (c *Config) NetworkPassphrase() (string, error) {
	switch c.Network {
	case "public":
		return network.PublicNetworkPassphrase, nil
	case "testnet":
		return network.TestNetworkPassphrase, nil
	case "futurenet":
		return network.FutureNetworkPassphrase, nil
	default:
		return "", fmt.Errorf("unknown network: %s", c.Network)
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return fallback
}
