package auth

import (
	"errors"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinPasswordLen is the fewest characters a new password may have.
	MinPasswordLen = 8
	// maxPasswordBytes is bcrypt's limit: it reads no further than 72 bytes,
	// so a longer password would be silently cut short.
	maxPasswordBytes = 72
)

var (
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
	ErrPasswordTooLong  = errors.New("password must be at most 72 bytes")
)

// ValidatePassword checks a new password against the policy. Sign-in does not
// use it: existing passwords keep working.
func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLen {
		return ErrPasswordTooShort
	}
	if len(pw) > maxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// BurnPassword spends the time of a real password check on a hash nobody has.
// Sign-in calls it when the email is unknown, so a wrong email answers as
// slowly as a wrong password and the response time does not tell a caller
// which emails have accounts.
func BurnPassword(pw string) {
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("no-such-account"), bcrypt.DefaultCost)
	})
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
}
