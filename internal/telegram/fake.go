package telegram

import (
	"context"
	"fmt"
	"sync"
)

// Sent is one recorded message.
type Sent struct {
	ChatID  int64
	Text    string
	Buttons [][]Button
	Photo   string
}

// FakeAPI records calls and lets tests script failures. It never touches the network.
type FakeAPI struct {
	mu        sync.Mutex
	Messages  []Sent
	Edits     []Sent
	Invoices  []InvoiceParams
	Refunds   []string
	PreCheck  map[string]bool
	Members   map[string]string // "chat:user" -> status
	Commands  map[string]map[string]string
	WebhookTo string
	// FailSend returns an error for a chat (ErrBlocked, RateLimited...).
	FailSend map[int64]error
}

func NewFake() *FakeAPI {
	return &FakeAPI{PreCheck: map[string]bool{}, Members: map[string]string{}, FailSend: map[int64]error{}, Commands: map[string]map[string]string{}}
}

func (f *FakeAPI) SendMessage(ctx context.Context, chatID int64, text string, o SendOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.FailSend[chatID]; err != nil {
		return err
	}
	f.Messages = append(f.Messages, Sent{ChatID: chatID, Text: text, Buttons: o.Buttons, Photo: o.PhotoURL})
	return nil
}

func (f *FakeAPI) EditMessage(ctx context.Context, chatID, messageID int64, text string, o SendOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Edits = append(f.Edits, Sent{ChatID: chatID, Text: text, Buttons: o.Buttons})
	return nil
}

func (f *FakeAPI) CreateInvoiceLink(ctx context.Context, p InvoiceParams) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Invoices = append(f.Invoices, p)
	return fmt.Sprintf("https://t.me/$fake-invoice-%d", len(f.Invoices)), nil
}

func (f *FakeAPI) AnswerPreCheckoutQuery(ctx context.Context, id string, ok bool, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PreCheck[id] = ok
	return nil
}

func (f *FakeAPI) AnswerCallbackQuery(ctx context.Context, id, text string) error { return nil }

func (f *FakeAPI) RefundStarPayment(ctx context.Context, userID int64, chargeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Refunds = append(f.Refunds, chargeID)
	return nil
}

func (f *FakeAPI) GetChatMemberStatus(ctx context.Context, chat string, userID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.Members[fmt.Sprintf("%s:%d", chat, userID)]; ok {
		return s, nil
	}
	return "left", nil
}

func (f *FakeAPI) SetWebhook(ctx context.Context, url, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.WebhookTo = url
	return nil
}

func (f *FakeAPI) SetCommands(ctx context.Context, lang string, cmds map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Commands[lang] = cmds
	return nil
}

// MessagesTo returns what was sent to a chat.
func (f *FakeAPI) MessagesTo(chatID int64) []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Sent
	for _, m := range f.Messages {
		if m.ChatID == chatID {
			out = append(out, m)
		}
	}
	return out
}

// Reset clears the recorded calls.
func (f *FakeAPI) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Messages, f.Invoices, f.Refunds = nil, nil, nil
}
