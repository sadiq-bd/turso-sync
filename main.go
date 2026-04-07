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
			runWithReconnect(ctx, cfg)
		}(dbCfg)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n🛑 Shutdown signal received. Stopping all sync engines...")
	cancel()
	wg.Wait()
	fmt.Println("✅ All sync engines stopped cleanly.")
}

func runWithReconnect(ctx context.Context, cfg DBConfig) {
	for {
		if ctx.Err() != nil {
			return
		}

		log.Printf("[%s] Starting / reconnecting...", cfg.Name)
		err := runSyncOnce(ctx, cfg)

		if ctx.Err() != nil {
			return
		}

		waitDuration := 5 * time.Second
		if err != nil {
			waitDuration = 10 * time.Second
			log.Printf("⚠️  [%s] Sync session ended with error: %v → will reconnect in 10s", cfg.Name, err)
		} else {
			log.Printf("[%s] Sync session ended cleanly → reconnecting in 5s", cfg.Name)
		}

		select {
		case <-time.After(waitDuration):
		case <-ctx.Done():
			return
		}
	}
}

func runSyncOnce(ctx context.Context, cfg DBConfig) error {
	syncInterval := 60 * time.Second
	if cfg.SyncInterval != "" {
		if d, err := time.ParseDuration(cfg.SyncInterval); err == nil {
			syncInterval = d
		}
	}

	if cfg.LocalDBPath == "" {
		cfg.LocalDBPath = getDefaultLocalDBPath(cfg.Name)
	}

	// Ensure local db directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.LocalDBPath), 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	// Create connector with retry
	connector, err := createConnectorWithRetry(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to create connector: %w", err)
	}
	defer connector.Close()

	db := sql.OpenDB(connector)
	defer db.Close()

	log.Printf("✅ [%s] Connected successfully", cfg.Name)

	// Initial sync
	log.Printf("[%s] Performing initial sync...", cfg.Name)
	if _, err := connector.Sync(); err != nil {
		log.Printf("⚠️  [%s] Initial sync failed: %v", cfg.Name, err)
	} else {
		log.Printf("✅ [%s] Initial sync completed", cfg.Name)
	}

	// Health monitor & Manual Sync Loop
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	consecutiveFailures := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// 1. Health check local db
			var count int
			err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'").Scan(&count)

			if err != nil {
				consecutiveFailures++
				log.Printf("⚠️  [%s] Local DB check failed (%d/3): %v", cfg.Name, consecutiveFailures, err)
				if consecutiveFailures >= 3 {
					return fmt.Errorf("too many local db check failures")
				}
				continue
			}

			// 2. Network Sync with timeout
			syncChan := make(chan error, 1)
			go func() {
				_, syncErr := connector.Sync()
				syncChan <- syncErr
			}()

			syncTimeout := syncInterval / 2
			if syncTimeout < 10*time.Second {
				syncTimeout = 10 * time.Second
			}

			select {
			case <-ctx.Done():
				return nil
			case syncErr := <-syncChan:
				if syncErr != nil {
					consecutiveFailures++
					log.Printf("⚠️  [%s] Network Sync failed (%d/3): %v", cfg.Name, consecutiveFailures, syncErr)
					if consecutiveFailures >= 3 {
						return fmt.Errorf("too many network sync failures: %w", syncErr)
					}
				} else {
					consecutiveFailures = 0
					log.Printf("📊 [%s] Healthy & Synced (%d tables)", cfg.Name, count)
				}
			case <-time.After(syncTimeout):
				consecutiveFailures++
				log.Printf("⚠️  [%s] Network Sync timed out (%d/3)", cfg.Name, consecutiveFailures)
				if consecutiveFailures >= 3 {
					return fmt.Errorf("network sync persistently timed out")
				}
			}
		}
	}
}

func createConnectorWithRetry(ctx context.Context, cfg DBConfig) (*libsql.Connector, error) {
	for attempt := 1; ; attempt++ {
		connector, err := libsql.NewEmbeddedReplicaConnector(
			cfg.LocalDBPath,
			cfg.TursoURL,
			libsql.WithAuthToken(cfg.TursoAuthToken),
		)
		if err == nil {
			return connector, nil
		}

		log.Printf("⚠️  [%s] Connector attempt %d failed: %v", cfg.Name, attempt, err)

		backoff := time.Duration(attempt*3) * time.Second
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func loadConfig() Config {
	configPath := getConfigFilePath()

	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("❌ Could not read config file: %s\nPlease create it with your database configurations.\nError: %v", configPath, err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		log.Fatalf("❌ Failed to parse config.json: %v", err)
	}

	// Basic validation
	for i := range config.Databases {
		if config.Databases[i].Name == "" {
			config.Databases[i].Name = fmt.Sprintf("db-%d", i+1)
		}
		if config.Databases[i].TursoURL == "" || config.Databases[i].TursoAuthToken == "" {
			log.Fatalf("❌ Database '%s' is missing turso_url or turso_auth_token", config.Databases[i].Name)
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
