package tgauth

import (
	"strconv"
	"testing"
	"time"
)

const tok = "123456:TEST-TOKEN"

func fields(now time.Time) map[string]string {
	return map[string]string{
		"user":        `{"id":777,"first_name":"Ann","username":"ann","language_code":"ru","is_premium":true}`,
		"auth_date":   strconv.FormatInt(now.Unix(), 10),
		"start_param": "ref_5",
		"query_id":    "AAH",
	}
}

func TestValidateOK(t *testing.T) {
	now := time.Now()
	d, err := Validate(Sign(tok, fields(now)), tok, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.User.ID != 777 || d.User.Username != "ann" || !d.User.IsPremium || d.StartParam != "ref_5" {
		t.Fatalf("bad parse: %+v", d)
	}
}

func TestValidateRejects(t *testing.T) {
	now := time.Now()
	good := Sign(tok, fields(now))
	if _, err := Validate(good, "999:OTHER", time.Hour, now); err != ErrInvalid {
		t.Fatalf("wrong bot token must fail, got %v", err)
	}
	if _, err := Validate(good+"&extra=1", tok, time.Hour, now); err != ErrInvalid {
		t.Fatalf("tampered data must fail, got %v", err)
	}
	if _, err := Validate("user=%7B%7D&auth_date=1", tok, time.Hour, now); err != ErrInvalid {
		t.Fatalf("missing hash must fail, got %v", err)
	}
	old := Sign(tok, fields(now.Add(-48*time.Hour)))
	if _, err := Validate(old, tok, 24*time.Hour, now); err != ErrExpired {
		t.Fatalf("old initData must be expired, got %v", err)
	}
	future := Sign(tok, fields(now.Add(2*time.Hour)))
	if _, err := Validate(future, tok, 24*time.Hour, now); err != ErrExpired {
		t.Fatalf("future initData must fail, got %v", err)
	}
	if _, err := Validate("", tok, time.Hour, now); err == nil {
		t.Fatal("empty must fail")
	}
}
