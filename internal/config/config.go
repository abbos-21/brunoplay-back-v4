// Package config reads the process configuration from environment variables.
// Secrets (bot token, JWT secret, wallet keys) only ever come from the environment, never from files in the repository.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env          string // dev | staging | production
	HTTPAddr     string
	DatabaseURL  string
	DBMaxConns   int32
	JWTSecret    string
	AdminJWTSecr string

	BotToken       string
	BotUsername    string
	BotAppName     string // short name of the bot's Mini App (t.me/<bot>/<app>)
	BotWebhookSec  string // secret path / header token for the Telegram webhook
	PublicAPIURL   string
	WebAppURL      string
	AdsWebhookSec  string
	InitDataMaxAge time.Duration

	DevAuth    bool // only allowed outside production
	AutoPayout bool // master switch for automatic TON payouts (off by default)

	TonAPIURL     string
	TonAPIKey     string
	TonWallet     string // project wallet that receives top-ups
	PayoutSeed    string // hot wallet secret (only if AutoPayout)
	PayoutWalletV string // v4r2 | v5r1
	TonTestnet    bool
	AlertChatID   int64
	CORSOrigins   []string
	TrustedProxy  bool
	// request limits per second and burst (per client address, per player, per address for sign-in)
	IPRate, PlayerRate, LoginRate    float64
	IPBurst, PlayerBurst, LoginBurst int
	ShutdownGrace                    time.Duration
}

func (c Config) IsProd() bool { return c.Env == "production" }

func Load() (Config, error) {
	c := Config{
		Env:            get("APP_ENV", "dev"),
		HTTPAddr:       get("HTTP_ADDR", ":8080"),
		DatabaseURL:    get("DATABASE_URL", ""),
		JWTSecret:      get("JWT_SECRET", ""),
		AdminJWTSecr:   get("ADMIN_JWT_SECRET", ""),
		BotToken:       get("BOT_TOKEN", ""),
		BotUsername:    get("BOT_USERNAME", "ImTomsBot"),
		BotAppName:     get("BOT_APP_NAME", "app"),
		BotWebhookSec:  get("BOT_WEBHOOK_SECRET", ""),
		PublicAPIURL:   get("PUBLIC_API_URL", ""),
		WebAppURL:      get("WEB_APP_URL", ""),
		AdsWebhookSec:  get("ADSGRAM_WEBHOOK_SECRET", ""),
		InitDataMaxAge: getDur("INITDATA_MAX_AGE", 24*time.Hour),
		DevAuth:        getBool("DEV_AUTH", false),
		AutoPayout:     getBool("AUTO_PAYOUT", false),
		TonAPIURL:      get("TON_API_URL", "https://toncenter.com/api/v2"),
		TonAPIKey:      get("TON_API_KEY", ""),
		TonWallet:      get("TON_WALLET", ""),
		PayoutSeed:     get("PAYOUT_WALLET_SEED", ""),
		PayoutWalletV:  get("PAYOUT_WALLET_VERSION", "v4r2"),
		TonTestnet:     getBool("TON_TESTNET", false),
		TrustedProxy:   getBool("TRUSTED_PROXY", true),
		IPRate:         getFloat("IP_RATE", 30),
		IPBurst:        int(getFloat("IP_BURST", 90)),
		PlayerRate:     getFloat("PLAYER_RATE", 15),
		PlayerBurst:    int(getFloat("PLAYER_BURST", 45)),
		LoginRate:      getFloat("LOGIN_RATE", 0.5),
		LoginBurst:     int(getFloat("LOGIN_BURST", 20)),
		ShutdownGrace:  getDur("SHUTDOWN_GRACE", 20*time.Second),
	}
	n, _ := strconv.Atoi(get("DB_MAX_CONNS", "20"))
	c.DBMaxConns = int32(n)
	if v := get("ALERT_CHAT_ID", ""); v != "" {
		c.AlertChatID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := get("CORS_ORIGINS", ""); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.CORSOrigins = append(c.CORSOrigins, o)
			}
		}
	}
	return c, c.Validate()
}

// Validate refuses unsafe combinations, above all in production.
func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET must be at least 32 characters"))
	}
	if len(c.AdminJWTSecr) < 32 {
		errs = append(errs, errors.New("ADMIN_JWT_SECRET must be at least 32 characters"))
	}
	if c.IsProd() {
		if c.DevAuth {
			errs = append(errs, errors.New("DEV_AUTH must not be enabled in production"))
		}
		if c.BotToken == "" {
			errs = append(errs, errors.New("BOT_TOKEN is required in production"))
		}
		if c.AdsWebhookSec == "" {
			errs = append(errs, errors.New("ADSGRAM_WEBHOOK_SECRET is required in production"))
		}
		if c.BotWebhookSec == "" {
			errs = append(errs, errors.New("BOT_WEBHOOK_SECRET is required in production"))
		}
		if c.JWTSecret == c.AdminJWTSecr {
			errs = append(errs, errors.New("JWT_SECRET and ADMIN_JWT_SECRET must differ"))
		}
	}
	if c.AutoPayout && c.PayoutSeed == "" {
		errs = append(errs, errors.New("AUTO_PAYOUT needs PAYOUT_WALLET_SEED"))
	}
	return errors.Join(errs...)
}

func get(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func getBool(k string, def bool) bool {
	v := strings.ToLower(get(k, ""))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func getFloat(k string, def float64) float64 {
	if v := get(k, ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getDur(k string, def time.Duration) time.Duration {
	if v := get(k, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		fmt.Fprintf(os.Stderr, "config: bad duration %s=%q, using default\n", k, v)
	}
	return def
}
