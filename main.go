package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/tursodatabase/go-libsql"
)

type DBConfig struct {
	Name          string `json:"name"`
	TursoURL      string `json:"turso_url"`
	TursoAuthToken string `json:"turso_auth_token"`
	LocalDBPath   string `json:"local_db_path,omitempty"`
	SyncInterval  string `json:"sync_interval,omitempty"`
}

type Config struct {
	Databases []DBConfig `json:"databases"`
}

func main() {
	config := loadConfig()

	if len(config.Databases) == 0 {
		log.Fatal("❌ No databases configured in config.json")
	}

	fmt.Printf("🚀 Turso Multi-DB Sync Started (%d databases)\n\n", len(config.Databases))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	for _, dbCfg := range config.Databases {
		wg.Add(1)
		go func(cfg DBConfig) {
			defer wg.Done()
			startSyncForDB(ctx, cfg)
		}(dbCfg)
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n🛑 Shutdown signal received. Stopping all sync engines...")
	cancel()   // Tell all goroutines to stop
	wg.Wait()  // Wait until they actually finish

	fmt.Println("✅ All sync engines stopped cleanly.")
	fmt.Println("   Your local backups are safe.")
}

func startSyncForDB(ctx context.Context, cfg DBConfig) {
	// Recover from any unexpected panic in this goroutine
	defer func() {
		if r := recover(); r != nil {
			log.Printf("❌ [%s] PANIC recovered: %v", cfg.Name, r)
		}
	}()

	// Parse sync interval
	syncInterval := 60 * time.Second
	if cfg.SyncInterval != "" {
		if d, err := time.ParseDuration(cfg.SyncInterval); err == nil {
			syncInterval = d
		} else {
			log.Printf("Warning [%s]: Invalid sync_interval '%s' → using 60s", cfg.Name, cfg.SyncInterval)
		}
	}

	// Default local path
	if cfg.LocalDBPath == "" {
		cfg.LocalDBPath = getDefaultLocalDBPath(cfg.Name)
	}

	log.Printf("[%s] Starting sync | Local: %s | Interval: %v", cfg.Name, cfg.LocalDBPath, syncInterval)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.LocalDBPath), 0755); err != nil {
		log.Printf("❌ [%s] Failed to create directory: %v", cfg.Name, err)
		return
	}

	connector, err := libsql.NewEmbeddedReplicaConnector(
		cfg.LocalDBPath,
		cfg.TursoURL,
		libsql.WithAuthToken(cfg.TursoAuthToken),
		libsql.WithSyncInterval(syncInterval),
	)
	if err != nil {
		log.Printf("❌ [%s] Failed to create connector: %v", cfg.Name, err)
		return
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	// Initial sync
	log.Printf("[%s] Performing initial sync...", cfg.Name)
	if _, err := connector.Sync(); err != nil {
		log.Printf("⚠️  [%s] Initial sync failed (local file is still safe): %v", cfg.Name, err)
	} else {
		log.Printf("✅ [%s] Initial sync completed", cfg.Name)
	}

	log.Printf("✅ [%s] Background sync is now active", cfg.Name)

	// Health monitor (stops when context is cancelled)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[%s] Stopping health monitor", cfg.Name)
			return
		case <-ticker.C:
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'").Scan(&count); err != nil {
				log.Printf("Health check [%s] failed: %v", cfg.Name, err)
			} else {
				log.Printf("📊 [%s] Healthy (%d tables)", cfg.Name, count)
			}
		}
	}
}

func loadConfig() Config {
	configPath := getConfigFilePath()

	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("❌ Could not read config file: %s\nError: %v\nPlease create the config.json file.", configPath, err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatalf("❌ Failed to parse config.json: %v", err)
	}

	// Basic validation
	for i, db := range config.Databases {
		if db.Name == "" {
			db.Name = fmt.Sprintf("db-%d", i+1)
		}
		if db.TursoURL == "" || db.TursoAuthToken == "" {
			log.Fatalf("❌ Database '%s' is missing turso_url or turso_auth_token", db.Name)
		}
	}

	return config
}

func getConfigFilePath() string {
	return filepath.Join(getConfigDir(), "config.json")
}

func getDefaultLocalDBPath(name string) string {
	return filepath.Join(getConfigDir(), name+".db")
}

func getConfigDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "turso-sync")
}
