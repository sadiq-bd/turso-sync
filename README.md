<p align="left">
  <img src="https://api.sadiq.workers.dev/app/github/repo/turso-sync/views" alt="Repo Views" />
</p>

# Turso Multi-DB Sync

**Lightweight, reliable, incremental backup & sync engine for Turso databases using Embedded Replicas.**


This tool keeps one or more local SQLite replicas in sync with your Turso cloud databases.  
Only changed pages are transferred after the initial sync → very low bandwidth and cost.

Perfect for:
- Continuous incremental backups
- Fast local reads (microsecond latency)
- Disaster recovery
- Running as a background systemd service

---

## Features

- Sync **multiple Turso databases** simultaneously
- Automatic background sync with configurable interval
- Extremely low memory & CPU usage (written in Go)
- Proper graceful shutdown (Ctrl+C or service stop)
- Panic recovery per database
- Smart default paths (`~/.config/turso-sync/`)

---

## Prerequisites

- Go 1.22 or higher
- Turso account and database(s)
- `sqlite3` CLI (for manual verification)

---

## Installation

```bash
git clone https://github.com/yourusername/turso-sync.git
cd turso-sync

go mod tidy
go build -ldflags="-s -w" -o turso-sync main.go
```
## Configuration

```bash
mkdir -p ~/.config/turso-sync

nano ~/.config/turso-sync/config.json:
```

```json
{
  "databases": [
    {
      "name": "production",
      "turso_url": "libsql://your-prod-db.turso.io",
      "turso_auth_token": "eyJhbGciOi...",
      "local_db_path": "/var/lib/turso-backup/prod.db",
      "sync_interval": "60s"
    },
    {
      "name": "staging",
      "turso_url": "libsql://your-staging-db.turso.io",
      "turso_auth_token": "eyJhbGciOi...",
      "local_db_path": "/var/lib/turso-backup/stag.db",
      "sync_interval": "5m"
    }
  ]
}
```

## Usage

Run manually:

```bash
./turso-sync
```
Run as systemd service (recommended for 24/7):


## Verification

Check your local replicas:

```bash
# Quick integrity check
sqlite3 ~/.config/turso-sync/production.db "PRAGMA quick_check;"
```

## Security & Best Practices

- Never run as root
- Use the dedicated turso-sync user
- Enable Delete Protection in Turso dashboard
- Keep config.json out of Git
- Local .db files are your safe offline backups

## License

MIT
