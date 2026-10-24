package db

import (
	"strings"
	"testing"
)

func TestIsLocal(t *testing.T) {
	cases := map[string]bool{
		"postgres://dev:pw@localhost:5432/app?sslmode=disable":            true,
		"postgres://dev:pw@127.0.0.1/app":                                 true,
		"postgres://dev:pw@[::1]:5432/app":                                true,
		"postgres://dev:pw@db:5432/app":                                   true,
		"postgres://dev:pw@LOCALHOST/app":                                 true,
		"host=localhost user=dev dbname=app":                              true,
		"host=/var/run/postgresql dbname=app":                             true,
		"postgres://u:pw@ep-cool-123.ap-southeast-1.aws.neon.tech/app":    false,
		"postgres://u:pw@10.0.0.5/app":                                    false,
		"postgres://u:pw@localhost.evil.example/app":                      false,
		"postgres://u:pw@db.internal.example/app":                         false,
		"postgres://u:pw@localhost,ep-cool-123.neon.tech/app":             false, // a remote fallback host
		"host=ep-cool-123.ap-southeast-1.aws.neon.tech user=u dbname=app": false,
		"postgres://u:pw@localhost.:5432/app":                             false,
		"postgres://u:pw@127.0.0.1.nip.io/app":                            false,
	}
	for dsn, want := range cases {
		got, err := IsLocal(dsn)
		if err != nil {
			t.Errorf("IsLocal(%q): %v", dsn, err)
			continue
		}
		if got != want {
			t.Errorf("IsLocal(%q) = %v, want %v", dsn, got, want)
		}
	}
	if _, err := IsLocal("postgres://%zz"); err == nil {
		t.Error("a malformed DSN should be an error, not 'local'")
	}
}

func TestRefuseRemote(t *testing.T) {
	remote := "postgres://u:s3cret@ep-cool-123.neon.tech/app"
	err := RefuseRemote(remote, false, "reset the database")
	if err == nil {
		t.Fatal("remote database accepted")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks the password: %v", err)
	}
	if !strings.Contains(err.Error(), "ep-cool-123.neon.tech") {
		t.Errorf("error does not name the host: %v", err)
	}
	if err := RefuseRemote(remote, true, "reset the database"); err != nil {
		t.Errorf("explicit -allow-remote refused: %v", err)
	}
	if err := RefuseRemote("postgres://dev:pw@localhost/app", false, "reset the database"); err != nil {
		t.Errorf("local database refused: %v", err)
	}
}

func TestDescribeHidesCredentials(t *testing.T) {
	got, err := Describe("postgres://u:s3cret@ep-cool-123.neon.tech:5432/app")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "s3cret") || !strings.Contains(got, "ep-cool-123.neon.tech") || !strings.Contains(got, "app") {
		t.Errorf("Describe = %q", got)
	}
}
