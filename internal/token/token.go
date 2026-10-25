// Package token issues and verifies the signed session tokens of players and admins.
package token

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Kind string

const (
	Player Kind = "player"
	Admin  Kind = "admin"
)

type Claims struct {
	Kind Kind `json:"k"`
	jwt.RegisteredClaims
}

type Signer struct {
	secret []byte
	kind   Kind
}

func New(secret string, kind Kind) *Signer { return &Signer{secret: []byte(secret), kind: kind} }

// Issue returns a token for the subject (player or admin id) valid for ttl.
func (s *Signer) Issue(subject string, ttl time.Duration, now time.Time) (string, time.Time, error) {
	exp := now.Add(ttl)
	c := Claims{Kind: s.kind, RegisteredClaims: jwt.RegisteredClaims{
		Subject:   subject,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.secret)
	return tok, exp, err
}

var ErrBad = errors.New("token: invalid")
var ErrExpired = errors.New("token: expired")

// Verify returns the subject of a valid token of this signer's kind.
func (s *Signer) Verify(raw string, now time.Time) (string, error) {
	var c Claims
	t, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return "", ErrExpired
		}
		return "", ErrBad
	}
	if !t.Valid || c.Kind != s.kind || c.Subject == "" {
		return "", ErrBad
	}
	return c.Subject, nil
}
