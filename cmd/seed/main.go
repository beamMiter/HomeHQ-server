package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/beamMiter/HomeHQ-server/internal/demo"
)

func main() {
	// -dsn wins, then the DB_DSN environment variable, then the local dev
	// database. (Before this, -dsn was accepted and silently ignored.)
	flagDSN := flag.String("dsn", "", "database to seed (default: $DB_DSN, then the local dev database)")
	flag.Parse()
	dsn := *flagDSN
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		dsn = "postgres://dev:devpass@localhost:5432/homeservice?sslmode=disable"
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
