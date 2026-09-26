package common

import (
	"fmt"
	"os"
	"time"
)

// ControlConfig configures the control plane process. APIToken (PG_API_TOKEN)
// enables bearer authentication on client API routes; empty keeps the
// open local-dev behavior. Never logged, never persisted.
type ControlConfig struct {
	ListenAddr  string
	DatabaseURL string
	R2Endpoint  string
	R2Bucket    string
	AuthIssuer  string
	APIToken    string
}

// AgentConfig configures the node agent process. GAME_ROOT/TEMP_ROOT let
// operators place games and temp on different volumes (e.g. persistent
// block storage for /games); GPU_INDEX pins the visible NVIDIA device.
type AgentConfig struct {
	ControlURL   string
	NodeID       string
	DataDir      string
	GamesDir     string
	CacheDir     string
	StateDir     string
	TempRoot     string
	GPUIndex     int // -1 = unset (all devices visible)
	EnrollToken  string
	LeaseTTL     time.Duration
	HeartbeatInt time.Duration
}

// Defaults applied when env is absent (local dev friendly).
const (
	DefaultLeaseTTL     = 30 * time.Second
	DefaultHeartbeatInt = 10 * time.Second
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// LoadControlConfig reads control-plane config from the environment.
func LoadControlConfig() ControlConfig {
	return ControlConfig{
		ListenAddr:  getenv("PG_LISTEN_ADDR", ":8080"),
		DatabaseURL: os.Getenv("PG_DATABASE_URL"),
		R2Endpoint:  os.Getenv("PG_R2_ENDPOINT"),
		R2Bucket:    getenv("PG_R2_BUCKET", "personal-game-saves"),
		AuthIssuer:  getenv("PG_AUTH_ISSUER", "personal-game"),
		APIToken:    os.Getenv("PG_API_TOKEN"),
	}
}

// LoadAgentConfig reads agent config from the environment.
func LoadAgentConfig() (AgentConfig, error) {
	lease := DefaultLeaseTTL
	if v := os.Getenv("PG_LEASE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			lease = d
		}
	}
	hb := DefaultHeartbeatInt
	if v := os.Getenv("PG_HEARTBEAT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			hb = d
		}
	}
	dataDir := getenv("PG_DATA_DIR", "./data")
	gamesDir := getenv("PG_GAMES_DIR", dataDir+"/games")
	if root := os.Getenv("PG_GAME_ROOT"); root != "" {
		gamesDir = root
	}
	gpuIndex := -1
	if v := os.Getenv("PG_GPU_INDEX"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 {
			return AgentConfig{}, fmt.Errorf("agent: PG_GPU_INDEX must be a non-negative integer, got %q", v)
		}
		gpuIndex = n
	}
	return AgentConfig{
		ControlURL:   getenv("PG_CONTROL_URL", "http://127.0.0.1:8080"),
		NodeID:       os.Getenv("PG_NODE_ID"),
		DataDir:      dataDir,
		GamesDir:     gamesDir,
		CacheDir:     getenv("PG_CACHE_DIR", dataDir+"/cache"),
		StateDir:     getenv("PG_STATE_DIR", dataDir+"/state"),
		TempRoot:     getenv("PG_TEMP_ROOT", dataDir+"/tmp"),
		GPUIndex:     gpuIndex,
		EnrollToken:  os.Getenv("PG_ENROLL_TOKEN"),
		LeaseTTL:     lease,
		HeartbeatInt: hb,
	}, nil
}

// R2Config carries object-storage settings (never logged with secrets).
// Standard variables (also honored: PG_R2_ENDPOINT/PG_R2_BUCKET legacy):
//
//	R2_ACCOUNT_ID, R2_BUCKET, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY,
//	R2_REGION (default "auto"), R2_ENDPOINT (override for MinIO/tests).
type R2Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
}

// LoadR2Config reads object-storage config; Configured is false when the
// credentials are absent (callers keep the memory store / MinIO-less path).
func LoadR2Config() (R2Config, bool) {
	cfg := R2Config{
		Endpoint:  firstEnv("R2_ENDPOINT", "PG_R2_ENDPOINT"),
		Bucket:    firstEnv("R2_BUCKET", "PG_R2_BUCKET"),
		AccessKey: os.Getenv("R2_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		Region:    getenv("R2_REGION", "auto"),
	}
	if cfg.Endpoint == "" {
		if acct := os.Getenv("R2_ACCOUNT_ID"); acct != "" {
			cfg.Endpoint = "https://" + acct + ".r2.cloudflarestorage.com"
		}
	}
	if cfg.Bucket == "" {
		cfg.Bucket = "personal-game-saves"
	}
	configured := cfg.Endpoint != "" && cfg.AccessKey != "" && cfg.SecretKey != ""
	return cfg, configured
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Validate reports configuration problems before the process starts.
// Secrets are never logged; callers must redact them.
// An empty DatabaseURL is valid: the control plane runs on memory stores
// (local dev/tests); PostgreSQL activates when PG_DATABASE_URL is set.
func (c ControlConfig) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("control: LISTEN_ADDR empty")
	}
	return nil
}

// Validate reports agent configuration problems.
func (c AgentConfig) Validate() error {
	if c.ControlURL == "" {
		return fmt.Errorf("agent: PG_CONTROL_URL empty")
	}
	if c.DataDir == "" {
		return fmt.Errorf("agent: PG_DATA_DIR empty")
	}
	if c.LeaseTTL <= 0 || c.HeartbeatInt <= 0 {
		return fmt.Errorf("agent: lease/heartbeat durations must be positive, got %v/%v", c.LeaseTTL, c.HeartbeatInt)
	}
	if c.HeartbeatInt >= c.LeaseTTL {
		return fmt.Errorf("agent: heartbeat interval (%v) must be < lease TTL (%v)", c.HeartbeatInt, c.LeaseTTL)
	}
	return nil
}
