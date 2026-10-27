package settings

import (
	"encoding/json"
	"testing"
)

func TestSpinOddsCheck(t *testing.T) {
	good := []string{
		`{"TIER_1": {"apple": 40, "coins:500": 30, "stars:5": 10}}`,
		`{"TIER_1": {"coins:500": 1}, "TIER_2": {"heart": 2, "stars:100": 1}}`,
		`{"TIER_1": {"apple": 5, "cat": 0}}`, // a zero weight switches a prize off
	}
	for _, g := range good {
		if err := spinOddsCheck(json.RawMessage(g)); err != nil {
			t.Errorf("%s must be accepted: %v", g, err)
		}
	}
	bad := []string{
		`[]`,
		`{"TIER_1": {"coins:abc": 1}}`,
		`{"TIER_1": {"coins:": 1}}`,
		`{"TIER_1": {"coins:0": 1}}`,
		`{"TIER_1": {"coins:-5": 1}}`,
		`{"TIER_1": {"coins:1.5": 1}}`,
		`{"TIER_1": {"gems:5": 1}}`,
		`{"TIER_1": {"stars:5000000000": 1}}`,
		`{"TIER_1": {"apple": -1}}`,
		`{"TIER_1": {"coins:500": 0}}`, // nothing can ever be drawn
		`{"TIER_1": {}}`,
		`{"TIER_1": {" ": 1}}`,
	}
	for _, b := range bad {
		if err := spinOddsCheck(json.RawMessage(b)); err == nil {
			t.Errorf("%s must be refused", b)
		}
	}
}
