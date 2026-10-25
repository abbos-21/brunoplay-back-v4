// Package bot answers the Telegram bot's updates in Russian and English: /start (with invite links), /language, /notifications,
// /help, /stop, /resume, free text, and the "blocked / unblocked the bot" signal.
package bot

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"brunoplay/internal/apperr"
	"brunoplay/internal/db"
	"brunoplay/internal/notify"
	"brunoplay/internal/players"
	"brunoplay/internal/settings"
	"brunoplay/internal/telegram"
)

type Bot struct {
	pool      *pgxpool.Pool
	tg        telegram.API
	settings  *settings.Service
	webAppURL string
	// RememberInvite stores an invite that came through the bot before the person opened the app.
	RememberInvite func(ctx context.Context, telegramID, inviterID int64) error
}

func New(pool *pgxpool.Pool, tg telegram.API, st *settings.Service, webAppURL string) *Bot {
	return &Bot{pool: pool, tg: tg, settings: st, webAppURL: strings.TrimRight(webAppURL, "/")}
}

// SetAPI swaps the Telegram client (tests).
func (b *Bot) SetAPI(tg telegram.API) { b.tg = tg }

// Commands the bot registers in Telegram's menu.
func Commands(lang apperr.Lang) map[string]string {
	if lang == apperr.RU {
		return map[string]string{"start": "Открыть игру", "language": "Язык", "notifications": "Уведомления", "help": "Помощь", "stop": "Отключить уведомления", "resume": "Включить уведомления"}
	}
	return map[string]string{"start": "Open the game", "language": "Language", "notifications": "Notifications", "help": "Help", "stop": "Stop notifications", "resume": "Resume notifications"}
}

// Setup registers the webhook and the command menus (called once at start-up).
func (b *Bot) Setup(ctx context.Context, webhookURL, secret string) error {
	if err := b.tg.SetWebhook(ctx, webhookURL, secret); err != nil {
		return err
	}
	if err := b.tg.SetCommands(ctx, "", Commands(apperr.EN)); err != nil {
		return err
	}
	return b.tg.SetCommands(ctx, "ru", Commands(apperr.RU))
}

type person struct {
	tgID   int64
	lang   apperr.Lang
	exists bool
	id     int64
	off    map[string]bool
	allOff bool
}

func langFromCode(code string) apperr.Lang { return apperr.ParseLang(code) }

func (b *Bot) who(ctx context.Context, u telegram.User) (*person, error) {
	p := &person{tgID: u.ID, lang: langFromCode(u.LanguageCode), off: map[string]bool{}}
	var lang string
	var off []byte
	err := b.pool.QueryRow(ctx, `SELECT id, language, notif_off, notif_all_off FROM players WHERE telegram_id=$1 AND deleted_at IS NULL`, u.ID).Scan(&p.id, &lang, &off, &p.allOff)
	if err == nil {
		p.exists, p.lang = true, apperr.ParseLang(lang)
		var list []string
		_ = json.Unmarshal(off, &list)
		for _, c := range list {
			p.off[c] = true
		}
	} else if !db.IsNoRows(err) {
		return nil, err
	}
	return p, nil
}

// Handle processes one update.
func (b *Bot) Handle(ctx context.Context, u *telegram.Update) error {
	switch {
	case u.MyChatMember != nil:
		return b.chatMember(ctx, u.MyChatMember)
	case u.CallbackQuery != nil:
		return b.callback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.From != nil && u.Message.Chat.Type == "private":
		return b.message(ctx, u.Message)
	}
	return nil
}

func (b *Bot) chatMember(ctx context.Context, m *telegram.ChatMemberUpdate) error {
	if m.Chat.Type != "private" {
		return nil
	}
	blocked := m.NewChatMember.Status == "kicked" || m.NewChatMember.Status == "left"
	_, err := b.pool.Exec(ctx, `UPDATE players SET bot_blocked=$2 WHERE telegram_id=$1`, m.From.ID, blocked)
	return err
}

func (b *Bot) open(lang apperr.Lang, btn string) [][]telegram.Button {
	if b.webAppURL == "" {
		return nil
	}
	return [][]telegram.Button{{{Text: btn, WebApp: b.webAppURL + "/"}}}
}

func (b *Bot) openBtn(lang apperr.Lang) string {
	if lang == apperr.RU {
		return "Открыть игру"
	}
	return "Open game"
}

func (b *Bot) message(ctx context.Context, m *telegram.Message) error {
	p, err := b.who(ctx, *m.From)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(m.Text)
	cmd, arg := splitCommand(text)
	switch cmd {
	case "/start":
		return b.start(ctx, m, p, arg)
	case "/language":
		return b.languageMenu(ctx, m.Chat.ID, p)
	case "/notifications":
		return b.notificationsMenu(ctx, m.Chat.ID, 0, p)
	case "/stop":
		return b.setAllOff(ctx, m.Chat.ID, p, true)
	case "/resume":
		return b.setAllOff(ctx, m.Chat.ID, p, false)
	default: // /help and anything else
		return b.help(ctx, m.Chat.ID, p)
	}
}

func splitCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	parts := strings.SplitN(text, " ", 2)
	cmd := strings.ToLower(parts[0])
	if i := strings.Index(cmd, "@"); i > 0 { // /start@ImTomsBot
		cmd = cmd[:i]
	}
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return cmd, arg
}

func (b *Bot) start(ctx context.Context, m *telegram.Message, p *person, payload string) error {
	if _, err := b.pool.Exec(ctx, `INSERT INTO bot_users (telegram_id, lang_code) VALUES ($1,$2) ON CONFLICT (telegram_id) DO NOTHING`, p.tgID, m.From.LanguageCode); err != nil {
		return err
	}
	if _, err := b.pool.Exec(ctx, `UPDATE players SET bot_started=TRUE, bot_blocked=FALSE WHERE telegram_id=$1`, p.tgID); err != nil {
		return err
	}
	if inviter, ok := players.ParseReferral(payload); ok && !p.exists && b.RememberInvite != nil {
		if err := b.RememberInvite(ctx, p.tgID, inviter); err != nil {
			return err
		}
	}
	txt := "🎣 <b>Welcome to BrunoPlay!</b>\n\nFish for coins, upgrade your gear, play mini-games and turn coins into real TON. Tap the button to start."
	if p.lang == apperr.RU {
		txt = "🎣 <b>Добро пожаловать в BrunoPlay!</b>\n\nЛовите рыбу ради монет, улучшайте снаряжение, играйте в мини-игры и меняйте монеты на настоящие TON. Нажмите кнопку, чтобы начать."
	}
	return b.tg.SendMessage(ctx, m.Chat.ID, txt, telegram.SendOpts{Buttons: b.open(p.lang, b.openBtn(p.lang))})
}

func (b *Bot) help(ctx context.Context, chatID int64, p *person) error {
	txt := "ℹ️ <b>BrunoPlay help</b>\n\n/start — open the game\n/language — change the language\n/notifications — choose what I tell you\n/stop and /resume — mute or unmute messages\n\nQuestions? Use the Support button."
	if p.lang == apperr.RU {
		txt = "ℹ️ <b>Помощь BrunoPlay</b>\n\n/start — открыть игру\n/language — сменить язык\n/notifications — выбрать уведомления\n/stop и /resume — выключить или включить сообщения\n\nЕсть вопросы? Нажмите «Поддержка»."
	}
	rows := b.open(p.lang, b.openBtn(p.lang))
	if link := b.settings.StrOr(ctx, settings.KeyLinkSupport, ""); link != "" {
		label := "Support"
		if p.lang == apperr.RU {
			label = "Поддержка"
		}
		rows = append(rows, []telegram.Button{{Text: label, URL: link}})
	}
	lang := "Language"
	if p.lang == apperr.RU {
		lang = "Язык"
	}
	rows = append(rows, []telegram.Button{{Text: lang, Data: "menu:lang"}})
	return b.tg.SendMessage(ctx, chatID, txt, telegram.SendOpts{Buttons: rows})
}

func (b *Bot) languageMenu(ctx context.Context, chatID int64, p *person) error {
	txt := "Choose your language:"
	if p.lang == apperr.RU {
		txt = "Выберите язык:"
	}
	return b.tg.SendMessage(ctx, chatID, txt, telegram.SendOpts{Buttons: [][]telegram.Button{{{Text: "🇬🇧 English", Data: "lang:EN"}, {Text: "🇷🇺 Русский", Data: "lang:RU"}}}})
}

func (b *Bot) menuText(p *person) (string, [][]telegram.Button) {
	ru := p.lang == apperr.RU
	title := "🔔 <b>Notifications</b>\nTap a topic to switch it on or off. Payment messages always arrive."
	if ru {
		title = "🔔 <b>Уведомления</b>\nНажмите на тему, чтобы включить или выключить. Сообщения о платежах приходят всегда."
	}
	var rows [][]telegram.Button
	for _, c := range notify.Switchable {
		name := notify.CategoryNames[c][0]
		if ru {
			name = notify.CategoryNames[c][1]
		}
		mark := "✅ "
		if p.allOff || p.off[string(c)] {
			mark = "🚫 "
		}
		rows = append(rows, []telegram.Button{{Text: mark + name, Data: "cat:" + string(c)}})
	}
	all := "Mute everything"
	if p.allOff {
		all = "Unmute everything"
	}
	if ru {
		all = "Выключить всё"
		if p.allOff {
			all = "Включить всё"
		}
	}
	rows = append(rows, []telegram.Button{{Text: all, Data: "all:toggle"}})
	return title, rows
}

// notificationsMenu sends the menu (or edits it in place when messageID is given).
func (b *Bot) notificationsMenu(ctx context.Context, chatID, messageID int64, p *person) error {
	if !p.exists {
		txt := "Open the game once first, then you can choose your notifications."
		if p.lang == apperr.RU {
			txt = "Сначала откройте игру, потом можно будет настроить уведомления."
		}
		return b.tg.SendMessage(ctx, chatID, txt, telegram.SendOpts{Buttons: b.open(p.lang, b.openBtn(p.lang))})
	}
	text, rows := b.menuText(p)
	if messageID != 0 {
		return b.tg.EditMessage(ctx, chatID, messageID, text, telegram.SendOpts{Buttons: rows})
	}
	return b.tg.SendMessage(ctx, chatID, text, telegram.SendOpts{Buttons: rows})
}

func (b *Bot) setAllOff(ctx context.Context, chatID int64, p *person, off bool) error {
	if p.exists {
		if _, err := b.pool.Exec(ctx, `UPDATE players SET notif_all_off=$2 WHERE id=$1`, p.id, off); err != nil {
			return err
		}
	}
	txt := map[bool]map[apperr.Lang]string{
		true:  {apperr.EN: "🔕 Notifications are off (payment messages still arrive). Send /resume to turn them on.", apperr.RU: "🔕 Уведомления выключены (сообщения о платежах всё равно приходят). Отправьте /resume, чтобы включить."},
		false: {apperr.EN: "🔔 Notifications are on again.", apperr.RU: "🔔 Уведомления снова включены."},
	}[off][p.lang]
	return b.tg.SendMessage(ctx, chatID, txt, telegram.SendOpts{})
}

func (b *Bot) callback(ctx context.Context, q *telegram.CallbackQuery) error {
	p, err := b.who(ctx, q.From)
	if err != nil {
		return err
	}
	var chatID, msgID int64
	if q.Message != nil {
		chatID, msgID = q.Message.Chat.ID, q.Message.MessageID
	} else {
		chatID = q.From.ID
	}
	data := q.Data
	switch {
	case data == "menu:lang":
		_ = b.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return b.languageMenu(ctx, chatID, p)
	case strings.HasPrefix(data, "lang:"):
		l := strings.ToUpper(strings.TrimPrefix(data, "lang:"))
		if l != "EN" && l != "RU" {
			return nil
		}
		if p.exists {
			if _, err := b.pool.Exec(ctx, `UPDATE players SET language=$2 WHERE id=$1`, p.id, l); err != nil {
				return err
			}
		}
		p.lang = apperr.ParseLang(l)
		_ = b.tg.AnswerCallbackQuery(ctx, q.ID, "OK")
		txt := "Language set to English 🇬🇧"
		if p.lang == apperr.RU {
			txt = "Язык изменён на русский 🇷🇺"
		}
		return b.tg.SendMessage(ctx, chatID, txt, telegram.SendOpts{Buttons: b.open(p.lang, b.openBtn(p.lang))})
	case strings.HasPrefix(data, "cat:"):
		if !p.exists {
			return b.tg.AnswerCallbackQuery(ctx, q.ID, "")
		}
		cat := strings.TrimPrefix(data, "cat:")
		valid := false
		for _, c := range notify.Switchable {
			if string(c) == cat {
				valid = true
			}
		}
		if !valid {
			return nil
		}
		if p.off[cat] {
			delete(p.off, cat)
		} else {
			p.off[cat] = true
		}
		if err := b.saveOff(ctx, p); err != nil {
			return err
		}
		_ = b.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return b.notificationsMenu(ctx, chatID, msgID, p)
	case data == "all:toggle":
		if !p.exists {
			return b.tg.AnswerCallbackQuery(ctx, q.ID, "")
		}
		p.allOff = !p.allOff
		if _, err := b.pool.Exec(ctx, `UPDATE players SET notif_all_off=$2 WHERE id=$1`, p.id, p.allOff); err != nil {
			return err
		}
		_ = b.tg.AnswerCallbackQuery(ctx, q.ID, "")
		return b.notificationsMenu(ctx, chatID, msgID, p)
	}
	return b.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (b *Bot) saveOff(ctx context.Context, p *person) error {
	list := []string{}
	for c := range p.off {
		list = append(list, c)
	}
	bs, _ := json.Marshal(list)
	_, err := b.pool.Exec(ctx, `UPDATE players SET notif_off=$2 WHERE id=$1`, p.id, bs)
	return err
}

// SetWebApp changes the Mini App address used by the buttons (tests).
func (b *Bot) SetWebApp(url string) { b.webAppURL = strings.TrimRight(url, "/") }
