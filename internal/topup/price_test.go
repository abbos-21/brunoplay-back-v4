package topup

import "testing"

func TestTonPriceHasTwoDecimals(t *testing.T) {
	const perTon = 138.0 // stars per 1 TON
	for _, c := range []struct {
		stars int64
		want  int64 // nanoTON
	}{
		{200, 1_450_000_000},    // 1.449... -> 1.45
		{500, 3_620_000_000},    // 3.623... -> 3.62
		{10000, 72_460_000_000}, // 72.463... -> 72.46
		{138, 1_000_000_000},    // exactly 1 TON stays 1
		{1, 10_000_000},         // 0.0072 -> never below 0.01
		{276, 2_000_000_000},    // exactly 2 TON stays 2
		{152, 1_100_000_000},    // 1.1014 -> 1.10
	} {
		if got := tonPrice(c.stars, perTon); got != c.want {
			t.Errorf("tonPrice(%d) = %d, want %d", c.stars, got, c.want)
		}
	}
	if tonPrice(100, 0) != 0 {
		t.Error("no rate, no price")
	}
}
