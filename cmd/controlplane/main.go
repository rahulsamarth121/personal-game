package main

// Control-plane entrypoint: validates config, seeds the game catalog from
// versioned JSON manifests, selects stores (PostgreSQL when PG_DATABASE_URL
// is set, memory otherwise; R2/MinIO object storage when R2_* credentials
// exist, memory otherwise), and serves the API.

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"

	_ "github.com/lib/pq"

	"github.com/personal-game/personal-game/internal/common"
	"github.com/personal-game/personal-game/internal/control/api"
	"github.com/personal-game/personal-game/internal/control/auth"
	"github.com/personal-game/personal-game/internal/control/catalog"
	"github.com/personal-game/personal-game/internal/control/nodes"
	controlSaves "github.com/personal-game/personal-game/internal/control/saves"
	controlSession "github.com/personal-game/personal-game/internal/control/session"
	"github.com/personal-game/personal-game/internal/store/object"
	"github.com/personal-game/personal-game/internal/store/postgres"
)

func main() {
	cfg := common.LoadControlConfig()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "controlplane: invalid config:", err)
		os.Exit(1)
	}
	cat := catalog.New()
	manifestDir := os.Getenv("PG_MANIFEST_DIR")
	if manifestDir == "" {
		manifestDir = "./games/manifests"
	}
	ids, err := cat.LoadDir(manifestDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "controlplane: catalog seed failed:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "controlplane: catalog seeded with", len(ids), "game(s):", ids)

	reg := nodes.New()
	sessMgr, savesMgr := wireStores(cfg, reg, cat)

	srv := api.NewFull(cat, reg, sessMgr, savesMgr)
	if cfg.EnrollToken != "" {
		srv.EnrollToken = cfg.EnrollToken
	}
	fmt.Fprintln(os.Stderr, "controlplane: listening on", cfg.ListenAddr)
	handler := auth.ClientAuthMiddleware(cfg.APIToken, srv)
	if err := http.ListenAndServe(cfg.ListenAddr, handler); err != nil {
		fmt.Fprintln(os.Stderr, "controlplane:", err)
		os.Exit(1)
	}
}

// wireStores selects PostgreSQL when PG_DATABASE_URL is set (migrating the
// schema first) and R2/MinIO object storage when R2_* credentials exist.
// Everything else falls back to memory stores so local runs need no cloud.
func wireStores(cfg common.ControlConfig, reg *nodes.Registry, cat *catalog.Registry) (*controlSession.Manager, *controlSaves.Manager) {
	var sessStore controlSession.Store = controlSession.NewMemoryStore()
	var saveStore controlSaves.Store = controlSaves.NewMemoryStore()
	if cfg.DatabaseURL != "" {
		db, err := sql.Open("postgres", cfg.DatabaseURL)
		if err != nil {
			fmt.Fprintln(os.Stderr, "controlplane: postgres open failed:", err)
			os.Exit(1)
		}
		if err := db.Ping(); err != nil {
			fmt.Fprintln(os.Stderr, "controlplane: postgres unreachable:", err)
			os.Exit(1)
		}
		if err := postgres.Migrate(db); err != nil {
			fmt.Fprintln(os.Stderr, "controlplane: migration failed:", err)
			os.Exit(1)
		}
		sessStore = postgres.NewSessionStore(db)
		saveStore = postgres.NewSaveStore(db)
		fmt.Fprintln(os.Stderr, "controlplane: stores=postgres")
	} else {
		fmt.Fprintln(os.Stderr, "controlplane: stores=memory (set PG_DATABASE_URL for postgres)")
	}

	var objStore object.Store = object.NewMemoryStore()
	if r2cfg, ok := common.LoadR2Config(); ok {
		objStore = object.NewR2Store(object.R2Config{
			Endpoint: r2cfg.Endpoint, Bucket: r2cfg.Bucket,
			AccessKey: r2cfg.AccessKey, SecretKey: r2cfg.SecretKey, Region: r2cfg.Region,
		})
		fmt.Fprintln(os.Stderr, "controlplane: objects=r2", r2cfg.Endpoint, "bucket", r2cfg.Bucket)
	} else {
		fmt.Fprintln(os.Stderr, "controlplane: objects=memory (set R2_* for R2/MinIO)")
	}

	sessMgr := controlSession.NewManager(sessStore, cat, reg)
	savesMgr := controlSaves.NewManager(saveStore, objStore, sessMgr)
	return sessMgr, savesMgr
}
