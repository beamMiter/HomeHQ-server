package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/beamMiter/HomeHQ-server/internal/db"
)

func main() {
	cmd := flag.String("cmd", "up", "goose command: up|down|reset|status")
	dsn := flag.String("dsn", "", "postgres DSN")
	allowRemote := flag.Bool("allow-remote", false, "let down/reset run on a database that is not on this machine")
	flag.Parse()

	if *dsn == "" {
		*dsn = os.Getenv("DB_DSN")
	}
	if *dsn == "" {
		*dsn = "postgres://dev:devpass@localhost:5432/homeservice?sslmode=disable"
	}

	// up and status are safe anywhere (up is how a deploy gets its schema).
	// down and reset throw data away, so they must not be able to reach a live
	// database just because DB_DSN was left set in the shell.
	if *cmd == "down" || *cmd == "reset" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") {
			log.Fatalf("refusing goose %s: APP_ENV=production", *cmd)
		}
		if err := db.RefuseRemote(*dsn, *allowRemote, "run goose "+*cmd); err != nil {
			log.Fatal(err)
		}
	}

	conn, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	goose.SetDialect("postgres")

	if err := goose.RunContext(context.Background(), *cmd, conn, "migrations"); err != nil {
		log.Fatalf("goose %s: %v", *cmd, err)
	}
}