// Command demospend fills the spending dashboard with sample data, or removes
// it again. Every row it makes is marked "(demo)" in its title; only those are
// ever removed.
//
//	go run ./cmd/demospend -dsn="$DB_DSN"          # replace the demo data with a fresh set
//	go run ./cmd/demospend -dsn="$DB_DSN" -clear   # remove the demo data
//
// There is no default database: say which one, so a sample set never lands in
// a database you did not mean. It writes only to this machine's databases.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/beamMiter/HomeHQ-server/internal/demo"
)

func main() {
	dsn := flag.String("dsn", "", "the database to write to (required)")
	clear := flag.Bool("clear", false, "remove the demo data instead of making it")
	months := flag.Int("months", 6, "how many months back to fill")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("say which database: -dsn=postgres://...")
	}
	u, err := url.Parse(*dsn)
	if err != nil {
		log.Fatalf("bad -dsn: %v", err)
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
	default:
		log.Fatalf("refusing to write sample data to %q: only this machine's databases", u.Hostname())
	}
	fmt.Printf("database: %s on %s (port %s)\n", u.Path[1:], u.Hostname(), u.Port())

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	if *clear {
		r, err := demo.ClearSpending(ctx, pool)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("removed %d demo bills and %d demo purchases\n", r.Bills, r.Purchases)
		return
	}
	r, err := demo.SeedSpending(ctx, pool, demo.SpendingParams{Now: time.Now(), Months: *months})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("made %d demo bills and %d demo purchases (run with -clear to remove them)\n", r.Bills, r.Purchases)
	_ = os.Stdout.Sync()
}
