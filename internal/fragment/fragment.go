// Package fragment asks fragment-api.uz what an amount of Telegram Stars costs in TON.
package fragment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client reads prices only; it never buys anything.
type Client struct {
	base string
	key  string
	http *http.Client
}

func New(baseURL, key string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), key: key, http: &http.Client{Timeout: 15 * time.Second}}
}

type pricingResp struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Result  struct {
		Amount int64 `json:"amount"`
		Price  struct {
			TON json.RawMessage `json:"ton"`
		} `json:"price"`
	} `json:"result"`
}

// StarsPriceNano is the price of `stars` Telegram Stars in nanoTON.
func (c *Client) StarsPriceNano(ctx context.Context, stars int64) (int64, error) {
	if stars <= 0 {
		return 0, fmt.Errorf("fragment: no stars to price")
	}
	body, _ := json.Marshal(map[string]int64{"amount": stars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/stars/pricing", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.key)
	res, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fragment: %w", redact(err, c.key))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var r pricingResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, fmt.Errorf("fragment: bad answer (HTTP %d)", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK || !r.OK {
		return 0, fmt.Errorf("fragment: HTTP %d %s %s", res.StatusCode, r.Code, r.Message)
	}
	return parseTon(r.Result.Price.TON)
}

// parseTon reads "0.3151" (or 0.3151) as nanoTON.
func parseTon(raw json.RawMessage) (int64, error) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, fmt.Errorf("fragment: no TON price in the answer")
	}
	return int64(math.Round(v * 1e9)), nil
}

// redact keeps the key out of an error text (a URL error can echo what was sent).
func redact(err error, key string) error {
	if key == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), key, "***"))
}
