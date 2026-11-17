package settings

import (
	"encoding/json"
	"math"
	"testing"
)

// The roulette that ships by default pays back less than the price: the weights are chances per million, the gifts have the star
// values of the design (1,000,000 / 500,000 / 400,000 / 20,000 / 15,000 / 10,000 / 8,000 / 7,500) and a coin is worth nothing.
func TestDefaultRoulettePrizesPayBackLessThanThePrice(t *testing.T) {
	value := map[string]float64{"frog": 1_000_000, "heart": 500_000, "cap": 400_000, "panda": 20_000, "apple": 15_000, "cat": 10_000, "ring": 8_000, "watch": 7_500}
	var total, ev float64
	list := DefaultRoulettePrizes()
	for _, p := range list {
		w := p["weight"].(float64)
		total += w
		if p["type"] == "gift" {
			v, ok := value[p["gift"].(string)]
			if !ok {
				t.Fatalf("unknown default gift %v", p["gift"])
			}
			ev += w * v
		}
	}
	if math.Abs(total-1_000_000) > 0.5 {
		t.Fatalf("the weights are per million: %v", total)
	}
	const price = 15.0
	if rtp := ev / total / price; rtp < 0.85 || rtp > 0.97 {
		t.Fatalf("the default roulette pays back %.1f%%", rtp*100)
	}
	raw, _ := json.Marshal(list)
	if err := roulettePrizesCheck(raw); err != nil {
		t.Fatalf("the default list must pass its own check: %v", err)
	}
}
