package topup

import "testing"

func TestTonPriceIsAWholeNumberOfTon(t *testing.T) {
	const perTon = 138.0 // stars per 1 TON
	for _, c := range []struct {
		stars int64
		want  int64 // nanoTON
	}{
		{200, 2e9},    // 1.449... -> 2
		{500, 4e9},    // 3.623... -> 4
		{10000, 73e9}, // 72.46... -> 73
		{138, 1e9},    // exactly 1 TON stays 1
		{1, 1e9},      // never below 1 TON
		{276, 2e9},    // exactly 2 TON stays 2
		{277, 3e9},    // a little over 2 TON -> 3
	} {
		if got := tonPrice(c.stars, perTon); got != c.want {
			t.Errorf("tonPrice(%d) = %d, want %d", c.stars, got, c.want)
		}
	}
	if tonPrice(100, 0) != 0 {
		t.Error("no rate, no price")
	}
}
