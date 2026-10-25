// Package telegram is a small Telegram Bot API client (webhook mode) plus the update types the backend reads.
// Everything goes through the API interface so tests can use FakeAPI and never touch the network.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---- updates (only the fields we use)

type Update struct {
	UpdateID         int64             `json:"update_id"`
	Message          *Message          `json:"message"`
	CallbackQuery    *CallbackQuery    `json:"callback_query"`
	PreCheckoutQuery *PreCheckoutQuery `json:"pre_checkout_query"`
	MyChatMember     *ChatMemberUpdate `json:"my_chat_member"`
}

type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	IsPremium    bool   `json:"is_premium"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID         int64              `json:"message_id"`
	From              *User              `json:"from"`
	Chat              Chat               `json:"chat"`
	Date              int64              `json:"date"`
	Text              string             `json:"text"`
	SuccessfulPayment *SuccessfulPayment `json:"successful_payment"`
	RefundedPayment   *RefundedPayment   `json:"refunded_payment"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type PreCheckoutQuery struct {
	ID             string `json:"id"`
	From           User   `json:"from"`
	Currency       string `json:"currency"`
	TotalAmount    int64  `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
}

type SuccessfulPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int64  `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}

type RefundedPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int64  `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}

type ChatMemberUpdate struct {
	Chat          Chat `json:"chat"`
	From          User `json:"from"`
	NewChatMember struct {
		Status string `json:"status"`
	} `json:"new_chat_member"`
}

// ---- API

type Button struct {
	Text   string `json:"text"`
	URL    string `json:"url,omitempty"`
	WebApp string `json:"-"` // Mini App URL (rendered as web_app)
	Data   string `json:"callback_data,omitempty"`
}

type SendOpts struct {
	Buttons    [][]Button // inline keyboard rows
	PhotoURL   string
	DisableWeb bool
}

type InvoiceParams struct {
	Title, Description, Payload string
	Stars                       int64 // price in Telegram Stars (XTR)
}

// API is what the backend needs from the Bot API.
type API interface {
	SendMessage(ctx context.Context, chatID int64, text string, o SendOpts) error
	EditMessage(ctx context.Context, chatID, messageID int64, text string, o SendOpts) error
	CreateInvoiceLink(ctx context.Context, p InvoiceParams) (string, error)
	AnswerPreCheckoutQuery(ctx context.Context, id string, ok bool, errMsg string) error
	AnswerCallbackQuery(ctx context.Context, id, text string) error
	RefundStarPayment(ctx context.Context, userID int64, chargeID string) error
	GetChatMemberStatus(ctx context.Context, chat string, userID int64) (string, error)
	SetWebhook(ctx context.Context, url, secret string) error
	SetCommands(ctx context.Context, lang string, cmds map[string]string) error
}

// Error kinds the sender reacts to.
var (
	ErrBlocked   = errors.New("telegram: bot was blocked by the user")
	ErrNotFound  = errors.New("telegram: chat not found")
	ErrForbidden = errors.New("telegram: forbidden")
)

type RateLimited struct{ RetryAfter time.Duration }

func (e RateLimited) Error() string {
	return fmt.Sprintf("telegram: rate limited, retry after %s", e.RetryAfter)
}

// Client is the real Bot API client.
type Client struct {
	token string
	base  string
	http  *http.Client
}

func NewClient(token string) *Client {
	return &Client{token: token, base: "https://api.telegram.org", http: &http.Client{Timeout: 15 * time.Second}}
}

// SetBase points the client at another server (tests).
func (c *Client) SetBase(u string) { c.base = strings.TrimRight(u, "/") }

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, _ := json.Marshal(params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var r apiResp
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("telegram %s: bad response (%d)", method, res.StatusCode)
	}
	if !r.OK {
		desc := strings.ToLower(r.Description)
		switch {
		case r.ErrorCode == 429:
			return RateLimited{RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
		case r.ErrorCode == 403 && (strings.Contains(desc, "blocked") || strings.Contains(desc, "deactivated") || strings.Contains(desc, "kicked")):
			return ErrBlocked
		case r.ErrorCode == 403:
			return ErrForbidden
		case r.ErrorCode == 400 && (strings.Contains(desc, "chat not found") || strings.Contains(desc, "user not found")):
			return ErrNotFound
		}
		return fmt.Errorf("telegram %s: %d %s", method, r.ErrorCode, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func keyboard(rows [][]Button) any {
	if len(rows) == 0 {
		return nil
	}
	out := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		r := make([]map[string]any, 0, len(row))
		for _, b := range row {
			m := map[string]any{"text": b.Text}
			switch {
			case b.WebApp != "":
				m["web_app"] = map[string]string{"url": b.WebApp}
			case b.URL != "":
				m["url"] = b.URL
			default:
				m["callback_data"] = b.Data
			}
			r = append(r, m)
		}
		out = append(out, r)
	}
	return map[string]any{"inline_keyboard": out}
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, o SendOpts) error {
	if o.PhotoURL != "" {
		p := map[string]any{"chat_id": chatID, "photo": o.PhotoURL, "caption": text, "parse_mode": "HTML"}
		if kb := keyboard(o.Buttons); kb != nil {
			p["reply_markup"] = kb
		}
		return c.call(ctx, "sendPhoto", p, nil)
	}
	p := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if kb := keyboard(o.Buttons); kb != nil {
		p["reply_markup"] = kb
	}
	return c.call(ctx, "sendMessage", p, nil)
}

func (c *Client) EditMessage(ctx context.Context, chatID, messageID int64, text string, o SendOpts) error {
	p := map[string]any{"chat_id": chatID, "message_id": messageID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if kb := keyboard(o.Buttons); kb != nil {
		p["reply_markup"] = kb
	}
	return c.call(ctx, "editMessageText", p, nil)
}

func (c *Client) CreateInvoiceLink(ctx context.Context, p InvoiceParams) (string, error) {
	var link string
	err := c.call(ctx, "createInvoiceLink", map[string]any{
		"title": p.Title, "description": p.Description, "payload": p.Payload, "currency": "XTR",
		"prices": []map[string]any{{"label": p.Title, "amount": p.Stars}},
	}, &link)
	return link, err
}

func (c *Client) AnswerPreCheckoutQuery(ctx context.Context, id string, ok bool, errMsg string) error {
	p := map[string]any{"pre_checkout_query_id": id, "ok": ok}
	if !ok {
		p["error_message"] = errMsg
	}
	return c.call(ctx, "answerPreCheckoutQuery", p, nil)
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

func (c *Client) RefundStarPayment(ctx context.Context, userID int64, chargeID string) error {
	return c.call(ctx, "refundStarPayment", map[string]any{"user_id": userID, "telegram_payment_charge_id": chargeID}, nil)
}

func (c *Client) GetChatMemberStatus(ctx context.Context, chat string, userID int64) (string, error) {
	var m struct {
		Status string `json:"status"`
	}
	err := c.call(ctx, "getChatMember", map[string]any{"chat_id": chat, "user_id": userID}, &m)
	return m.Status, err
}

func (c *Client) SetWebhook(ctx context.Context, url, secret string) error {
	return c.call(ctx, "setWebhook", map[string]any{"url": url, "secret_token": secret,
		"allowed_updates": []string{"message", "callback_query", "pre_checkout_query", "my_chat_member"}}, nil)
}

func (c *Client) SetCommands(ctx context.Context, lang string, cmds map[string]string) error {
	list := make([]map[string]string, 0, len(cmds))
	for k, v := range cmds {
		list = append(list, map[string]string{"command": k, "description": v})
	}
	p := map[string]any{"commands": list}
	if lang != "" {
		p["language_code"] = lang
	}
	return c.call(ctx, "setMyCommands", p, nil)
}
