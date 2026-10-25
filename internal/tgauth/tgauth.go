// Package tgauth validates Telegram Mini App initData (signature and age) exactly as Telegram documents it.
package tgauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type User struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	PhotoURL     string `json:"photo_url"`
	IsPremium    bool   `json:"is_premium"`
}

type InitData struct {
	User       User
	AuthDate   time.Time
	StartParam string
}

var (
	ErrInvalid = errors.New("initData: invalid signature")
	ErrExpired = errors.New("initData: expired")
)

// Validate checks the HMAC signature with the bot token and that auth_date is not older than maxAge.
func Validate(initData, botToken string, maxAge time.Duration, now time.Time) (*InitData, error) {
	vals, err := url.ParseQuery(initData)
	if err != nil {
		return nil, ErrInvalid
	}
	hash := vals.Get("hash")
	if hash == "" || botToken == "" {
		return nil, ErrInvalid
	}
	pairs := make([]string, 0, len(vals))
	for k, v := range vals {
		if k == "hash" {
			continue
		}
		pairs = append(pairs, k+"="+v[0])
	}
	sort.Strings(pairs)
	check := strings.Join(pairs, "\n")

	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	want := hex.EncodeToString(hmacSHA256(secret, []byte(check)))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(hash))) {
		return nil, ErrInvalid
	}

	secs, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil {
		return nil, ErrInvalid
	}
	at := time.Unix(secs, 0)
	if now.Sub(at) > maxAge || at.Sub(now) > 5*time.Minute {
		return nil, ErrExpired
	}

	var u User
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return nil, ErrInvalid
	}
	return &InitData{User: u, AuthDate: at, StartParam: vals.Get("start_param")}, nil
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// Sign builds a valid initData string (used by tests and by the dev sign-in).
func Sign(botToken string, fields map[string]string) string {
	pairs := make([]string, 0, len(fields))
	for k, v := range fields {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	hash := hex.EncodeToString(hmacSHA256(secret, []byte(strings.Join(pairs, "\n"))))
	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	q.Set("hash", hash)
	return q.Encode()
}
