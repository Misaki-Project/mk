package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/shiroha-a/mk/internal/config"
	"github.com/shiroha-a/mk/internal/migrationcompat"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	configPath := flag.String("config", ".config/default.yml", "path to configuration file")
	direction := flag.String("direction", "up", "migration direction: up or down")
	steps := flag.Int("steps", 0, "number of steps (0 = all)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	dbURL := migrationcompat.BuildDatabaseURL(cfg.DB.Host, cfg.DB.Port, cfg.DB.DB, cfg.DB.User, cfg.DB.Pass)

	ctx := context.Background()
	lock, err := migrationcompat.Acquire(ctx, dbURL)
	if err != nil {
		if errors.Is(err, migrationcompat.ErrLocked) {
			// 他の mk-go migration プロセスが実行中。パスワード等の付加情報を
			// 出さず固定文言のみで失敗させる。
			log.Print("another mk-go migration is already running")
			return err
		}
		return err
	}
	// run() 内の defer なので、main の log.Fatal (os.Exit) の前に必ず解放される。
	defer lock.Release(ctx)

	// 検出専用 preflight は上方向の migration 前だけ。down は SQL を逆順に
	// 適用するだけで fail-closed のままにする。
	if *direction == "up" {
		if err := lock.CheckPreflight(ctx); err != nil {
			return fmt.Errorf("migration compatibility preflight failed: %w", err)
		}
	}

	m, err := migrate.New("file://migration", dbURL)
	if err != nil {
		return fmt.Errorf("failed to create migrator: %w", err)
	}
	defer m.Close()

	switch *direction {
	case "up":
		if *steps > 0 {
			err = m.Steps(*steps)
		} else {
			err = m.Up()
		}
	case "down":
		if *steps > 0 {
			err = m.Steps(-*steps)
		} else {
			err = m.Down()
		}
	default:
		return fmt.Errorf("invalid direction %q", *direction)
	}

	if err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migration failed: %w", err)
	}

	if err == migrate.ErrNoChange {
		slog.Info("no migration changes to apply")
	} else {
		slog.Info("migration completed", "direction", *direction)
	}
	return nil
}
