package ton

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestAddressRoundTripAndChecks(t *testing.T) {
	var a Address
	for i := range a.Hash {
		a.Hash[i] = byte(i*7 + 3)
	}
	for _, bounce := range []bool{true, false} {
		f := a.Friendly(bounce)
		if len(f) != 48 {
			t.Fatalf("friendly length %d", len(f))
		}
		p, err := ParseAddress(f)
		if err != nil || p.Hash != a.Hash || p.Bounce != bounce || p.Workchain != 0 {
			t.Fatalf("round trip failed: %v %+v", err, p)
		}
		// the standard (non-url) alphabet is accepted too
		raw, _ := base64.RawURLEncoding.DecodeString(f)
		if _, err := ParseAddress(base64.StdEncoding.EncodeToString(raw)); err != nil {
			t.Fatalf("std base64: %v", err)
		}
	}
	p, err := ParseAddress(a.Raw())
	if err != nil || p.Hash != a.Hash {
		t.Fatalf("raw form: %v", err)
	}
	bad := []string{"", "hello", "0:zz", "5:" + strings.Repeat("a", 64), strings.Repeat("A", 48), a.Friendly(true)[:47]}
	f := a.Friendly(true)
	bad = append(bad, strings.Replace(f, string(f[10]), "Z", 1)) // a flipped character breaks the CRC (unless it was already Z)
	if f[10] == 'Z' {
		bad = bad[:len(bad)-1]
	}
	for _, s := range bad {
		if ValidAddress(s) {
			t.Errorf("%q must be invalid", s)
		}
	}
}

func TestCommentPayload(t *testing.T) {
	p, err := CommentPayloadBase64("bp123")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(p)
	if !strings.HasPrefix(string(raw), "\xb5\xee\x9c\x72") || !strings.HasSuffix(string(raw), "bp123") {
		t.Fatalf("unexpected boc %x", raw)
	}
	// header: magic, flags/size=1, off_bytes=1, cells=1, roots=1, absent=0, total size, root index 0, then the cell
	if raw[4] != 1 || raw[5] != 1 || raw[6] != 1 || raw[7] != 1 || raw[8] != 0 || int(raw[9]) != len(raw)-11 || raw[10] != 0 {
		t.Fatalf("header wrong: %x", raw[:12])
	}
	if raw[11] != 0 || int(raw[12]) != 2*(4+5) {
		t.Fatalf("cell descriptors wrong: %x", raw[11:13])
	}
	if _, err := CommentPayloadBase64(strings.Repeat("x", 200)); err == nil {
		t.Fatal("too long must fail")
	}
}

func TestNanoMaths(t *testing.T) {
	// 100,000 coins per TON: 10,000 coins = 0.1 TON
	if got := NanoFor(10_000, 100_000); got != 100_000_000 {
		t.Fatalf("got %d", got)
	}
	if got := NanoFor(1, 3); got != 333_333_333 {
		t.Fatalf("rounds down: %d", got)
	}
	if NanoFor(0, 10) != 0 || NanoFor(10, 0) != 0 {
		t.Fatal("zero guards")
	}
	for in, want := range map[int64]string{0: "0", 1: "0.000000001", 1_500_000_000: "1.5", 100_000_000: "0.1", 5_000_000: "0.005", 12 * Nano: "12"} {
		if got := TonString(in); got != want {
			t.Errorf("TonString(%d)=%s want %s", in, got, want)
		}
	}
	if NanoOfTon(0.005) != 5_000_000 {
		t.Fatalf("%d", NanoOfTon(0.005))
	}
}
