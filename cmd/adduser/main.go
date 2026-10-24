// Command adduser creates a household account, or resets one's password.
//
// Sign-up is closed in the API (anyone with an account can read all of the
// household's data), so this is how accounts are made. It talks to the database
// directly, so run it from a machine that may reach it.
//
// The password is never a flag (it would land in shell history and in the
// process list). Type it without echo into a variable, then run the command:
//
//	read -rs ADDUSER_PASSWORD && export ADDUSER_PASSWORD
//	go run ./cmd/adduser -dsn "$DB_DSN" -email mum@example.com -name "Mum"
//	go run ./cmd/adduser -dsn "$DB_DSN" -email me@example.com -name "Me" -role admin
//	go run ./cmd/adduser -dsn "$DB_DSN" -email mum@example.com -reset   # forgotten password
//	unset ADDUSER_PASSWORD
//
// A pipe works too: printf '%s' "$PW" | go run ./cmd/adduser ...
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/beamMiter/HomeHQ-server/internal/db"
	"github.com/beamMiter/HomeHQ-server/internal/user"
)

func main() {
	dsn := flag.String("dsn", "", "database to write to (default: $DB_DSN; there is no local default)")
	email := flag.String("email", "", "the account's email (required)")
	name := flag.String("name", "", "the person's name (required unless -reset)")
	role := flag.String("role", "member", `"member" or "admin"`)
	reset := flag.Bool("reset", false, "set a new password for an existing account instead of creating one")
	flag.Parse()

	if *dsn == "" {
		*dsn = os.Getenv("DB_DSN")
	}
	if *dsn == "" {
		log.Fatal("say which database: -dsn=postgres://... (or set DB_DSN)")
	}
	if *email == "" {
		log.Fatal("-email is required")
	}
	if !*reset && *name == "" {
		log.Fatal("-name is required")
	}

	password, err := readPassword()
	if err != nil {
		log.Fatal(err)
	}

	where, err := db.Describe(*dsn)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("database: %s\n", where)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	repo := user.Repo{DB: pool}

	if *reset {
		if err := repo.ResetAccountPassword(ctx, *email, password); err != nil {
			if errors.Is(err, user.ErrNotFound) {
				log.Fatalf("no account with email %s", *email)
			}
			log.Fatal(err)
		}
		fmt.Printf("password reset for %s; they are signed out everywhere\n", strings.ToLower(strings.TrimSpace(*email)))
		return
	}

	u, err := repo.CreateAccount(ctx, *name, *email, *role, password)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("created %s account %s <%s> (id %s)\n", u.Role, u.Name, u.Email, u.ID)
}

// readPassword takes the password from ADDUSER_PASSWORD, or from the first line
// of a pipe. It does not read a terminal: that would echo the password.
func readPassword() (string, error) {
	if pw := os.Getenv("ADDUSER_PASSWORD"); pw != "" {
		return pw, nil
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if pw := strings.TrimRight(line, "\r\n"); pw != "" {
			return pw, nil
		}
		if err != nil {
			return "", fmt.Errorf("no password on stdin: %w", err)
		}
	}
	return "", errors.New("no password: run `read -rs ADDUSER_PASSWORD && export ADDUSER_PASSWORD` first")
}
