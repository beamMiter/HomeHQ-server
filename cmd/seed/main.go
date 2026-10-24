package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/beamMiter/HomeHQ-server/internal/db"
	"github.com/beamMiter/HomeHQ-server/internal/demo"
)

func main() {
	// -dsn wins, then the DB_DSN environment variable, then the local dev
	// database. (Before this, -dsn was accepted and silently ignored.)
	flagDSN := flag.String("dsn", "", "database to seed (default: $DB_DSN, then the local dev database)")
	allowRemote := flag.Bool("allow-remote", false, "seed a database that is not on this machine")
	flag.Parse()
	dsn := *flagDSN
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		dsn = "postgres://dev:devpass@localhost:5432/homeservice?sslmode=disable"
	}

	// The seed makes accounts with passwords that are written in the source.
	// On a real server that is a back door, so it never runs there, and it
	// refuses any database that is not on this machine unless told otherwise.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") {
		log.Fatal("refusing to seed: APP_ENV=production (demo accounts have known passwords)")
	}
	if err := db.RefuseRemote(dsn, *allowRemote, "seed demo accounts and data"); err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	defer pool.Close()

	log.Println("seeding...")

	seedUsers(ctx, pool)
	seedNotes(ctx, pool)
	seedChores(ctx, pool)
	seedBills(ctx, pool)
	seedPurchases(ctx, pool)
	seedMedicine(ctx, pool)
	seedContractors(ctx, pool)

	// Half a year of paid bills and bought purchases, marked "(demo)", so the
	// spending dashboard has something to draw. Unlike the rows above this one
	// replaces its own earlier rows instead of adding to them.
	if r, err := demo.SeedSpending(ctx, pool, demo.SpendingParams{Now: time.Now(), Months: 6}); err != nil {
		log.Printf("⚠️ spending history not seeded: %v", err)
	} else {
		log.Printf("✅ spending history seeded: %d bills, %d purchases (remove with: make demo-spending-clear)", r.Bills, r.Purchases)
	}

	log.Println("done!")
}
