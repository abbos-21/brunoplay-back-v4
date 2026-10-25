// Package gamecfg reads the structured game settings (card tables, level rewards) into typed values.
package gamecfg

import (
	"context"

	"brunoplay/internal/apperr"
	"brunoplay/internal/settings"
)

const (
	CardMining   = "MINING_RATE"
	CardCapacity = "COIN_CAPACITY"
	MaxLevel     = 10
)

// Requirement is an extra condition of a card level (besides the price).
type Requirement struct {
	Type   string `json:"type"` // invite_friends | player_level | other_card_level | task_done
	Value  int64  `json:"value"`
	DescEN string `json:"desc_en"`
	DescRU string `json:"desc_ru"`
}

// CardLevel is one row of a card table. Cost is the price to reach THIS level (level 1 is free).
type CardLevel struct {
	Level        int           `json:"level"`
	Value        float64       `json:"value"` // coins per second (mining) or coins (capacity)
	Cost         int64         `json:"cost"`
	Currency     string        `json:"currency"` // COIN | STAR
	Requirements []Requirement `json:"requirements"`
}

type Config struct{ S *settings.Service }

func keyOf(card string) (string, error) {
	switch card {
	case CardMining:
		return settings.KeyCardsMining, nil
	case CardCapacity:
		return settings.KeyCardsCapacity, nil
	}
	return "", apperr.NotFound
}

// Card returns the level table of a card, or CONFIG_MISSING when the owner has not set it yet.
func (c Config) Card(ctx context.Context, card string) ([]CardLevel, error) {
	k, err := keyOf(card)
	if err != nil {
		return nil, err
	}
	return settings.Get[[]CardLevel](ctx, c.S, k)
}

// ValueAt returns the table value at a level (clamped to the table).
func ValueAt(t []CardLevel, level int) float64 {
	if len(t) == 0 {
		return 0
	}
	if level < 1 {
		level = 1
	}
	if level > len(t) {
		level = len(t)
	}
	return t[level-1].Value
}

// LevelReward is paid once when the player reaches Level.
type LevelReward struct {
	Level int   `json:"level"`
	Coins int64 `json:"coins"`
	Stars int64 `json:"stars"`
}

func (c Config) LevelRewards(ctx context.Context) (map[int]LevelReward, error) {
	rows, err := settings.Get[[]LevelReward](ctx, c.S, settings.KeyLevelRewards)
	if err != nil {
		return nil, err
	}
	out := map[int]LevelReward{}
	for _, r := range rows {
		out[r.Level] = r
	}
	return out, nil
}

// Level names (the badge names of the app).
var LevelNames = [MaxLevel][2]string{
	{"Ember", "Уголёк"}, {"Meadow", "Луг"}, {"Lagoon", "Лагуна"}, {"Silver", "Серебро"}, {"Gold", "Золото"},
	{"Ruby", "Рубин"}, {"Amethyst", "Аметист"}, {"Emerald", "Изумруд"}, {"Obsidian", "Обсидиан"}, {"Legend", "Легенда"},
}
