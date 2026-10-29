// Package notify is the bot's notification system: one queue, one catalogue of message types in Russian and English,
// player preferences, quiet hours, cooldowns, a daily cap, and a sender that respects Telegram's limits.
package notify

import (
	"html"

	"brunoplay/internal/apperr"
	"regexp"
	"strings"
	"time"
)

type Category string

const (
	CatFishing  Category = "fishing"
	CatDaily    Category = "daily"
	CatBoosters Category = "boosters"
	CatFriends  Category = "friends"
	CatGames    Category = "games"
	CatNews     Category = "news"
	CatPayments Category = "payments" // always on
)

// Categories a player can switch off, in the order of the /notifications menu.
var Switchable = []Category{CatFishing, CatDaily, CatBoosters, CatFriends, CatGames, CatNews}

// CategoryNames are the menu labels.
var CategoryNames = map[Category][2]string{
	CatFishing:  {"Fishing", "Рыбалка"},
	CatDaily:    {"Daily rewards", "Ежедневное"},
	CatBoosters: {"Boosters", "Бустеры"},
	CatFriends:  {"Friends", "Друзья"},
	CatGames:    {"Games & tournaments", "Игры и турниры"},
	CatNews:     {"News", "Новости"},
	CatPayments: {"Payments", "Платежи"},
}

// Type describes one kind of message.
type Type struct {
	Name     string
	Cat      Category
	Cooldown time.Duration // at least this long between two messages of the type to one player
	Page     string        // the Mini App page the button opens
	EN, RU   string        // texts with {placeholders}
	BtnEN    string
	BtnRU    string
}

func (t Type) Mandatory() bool { return t.Cat == CatPayments }

var types = map[string]Type{}

func def(t Type) { types[t.Name] = t }

func init() {
	def(Type{"tank_full", CatFishing, 20 * time.Minute, "/", "🎣 Your tank is full! Collect your coins.", "🎣 Ваш садок полон! Заберите монеты.", "Collect", "Забрать"})
	def(Type{"collect_reminder", CatFishing, 6 * time.Hour, "/", "💰 Your coins are waiting in the tank.", "💰 Ваши монеты ждут в садке.", "Collect", "Забрать"})
	def(Type{"fishing_idle", CatFishing, 12 * time.Hour, "/", "🎣 Time to start fishing and mine more coins!", "🎣 Пора на рыбалку — добывайте больше монет!", "Fishing", "Рыбалка"})
	def(Type{"autocollect_done", CatFishing, 3 * time.Hour, "/", "⚙️ Autocollect gathered {coins} coins for you and started a new run.", "⚙️ Автосбор собрал {coins} монет и начал новую рыбалку.", "Open game", "Открыть игру"})
	def(Type{"level_up", CatFishing, 0, "/level", "🏆 Level {level}! {reward}", "🏆 Уровень {level}! {reward}", "Levels", "Уровни"})
	def(Type{"booster_expiring", CatBoosters, 0, "/shop/boosters", "⏳ Your {booster} booster ends in {days} d. Renew it in the shop.", "⏳ Бустер «{booster}» закончится через {days} дн. Продлите его в магазине.", "Shop", "Магазин"})
	def(Type{"booster_expired", CatBoosters, 0, "/shop/boosters", "⌛ Your {booster} booster has expired. Renew it in the shop.", "⌛ Бустер «{booster}» закончился. Продлите его в магазине.", "Shop", "Магазин"})
	def(Type{"card_upgrade_ready", CatBoosters, 24 * time.Hour, "/shop", "⬆️ You can upgrade «{card}» now!", "⬆️ Теперь можно улучшить «{card}»!", "Shop", "Магазин"})

	def(Type{"streak_ready", CatDaily, 12 * time.Hour, "/", "📅 Your daily reward is ready!", "📅 Ваша ежедневная награда готова!", "Claim", "Забрать"})
	def(Type{"combo_ready", CatDaily, 12 * time.Hour, "/", "🧩 Today's Daily Combo is ready — find the right order!", "🧩 Ежедневное комбо готово — найдите верный порядок!", "Play combo", "Играть"})
	def(Type{"daily_ready", CatDaily, 12 * time.Hour, "/", "📅🧩 Your daily reward and the Daily Combo are ready!", "📅🧩 Ежедневная награда и комбо готовы!", "Open game", "Открыть игру"})
	def(Type{"streak_at_risk", CatDaily, 4 * time.Hour, "/", "🔥 Don't lose your streak of {streak} days — claim today's reward!", "🔥 Не потеряйте серию из {streak} дн. — заберите сегодняшнюю награду!", "Claim", "Забрать"})
	def(Type{"streak_lost", CatDaily, 20 * time.Hour, "/", "💔 Your streak was reset. Start a new one today!", "💔 Серия сброшена. Начните новую уже сегодня!", "Claim", "Забрать"})
	def(Type{"combo_reminder", CatDaily, 4 * time.Hour, "/", "⏰ Last chance to play today's Daily Combo.", "⏰ Последний шанс сыграть в сегодняшнее комбо.", "Play combo", "Играть"})
	def(Type{"task_new", CatDaily, 12 * time.Hour, "/tasks", "📋 New tasks are waiting for you!", "📋 Вас ждут новые задания!", "Tasks", "Задания"})

	def(Type{"friend_joined", CatFriends, 0, "/friends", "👥 {name} reached level 2 and is now your friend!", "👥 {name} достиг 2 уровня и теперь ваш друг!", "Friends", "Друзья"})
	def(Type{"friend_level_reward", CatFriends, 0, "/friends", "🎁 Your friend {name} reached level {level}: +{coins} coins.", "🎁 Ваш друг {name} достиг {level} уровня: +{coins} монет.", "Friends", "Друзья"})
	def(Type{"friend_topup_bonus", CatFriends, 0, "/friends", "⭐ Your friend {name} topped up the game: +{stars} stars for you.", "⭐ Ваш друг {name} пополнил игру: +{stars} звёзд вам.", "Friends", "Друзья"})

	def(Type{"tournament_start", CatGames, 0, "/games", "🏁 Tournament «{name}» has started! Join and win prizes.", "🏁 Турнир «{name}» начался! Участвуйте и выигрывайте призы.", "Games", "Игры"})
	def(Type{"tournament_ending", CatGames, 0, "/games", "⏰ Tournament «{name}» ends soon. Your place: {rank}.", "⏰ Турнир «{name}» скоро закончится. Ваше место: {rank}.", "Games", "Игры"})
	def(Type{"tournament_result", CatGames, 0, "/games", "🏆 Tournament «{name}»: you took place {place}! Prize: {prize}", "🏆 Турнир «{name}»: вы заняли {place} место! Приз: {prize}", "Games", "Игры"})
	def(Type{"gift_claim_delivered", CatGames, 0, "/", "🎁 Your gift «{gift}» was delivered!", "🎁 Ваш подарок «{gift}» отправлен!", "Open game", "Открыть игру"})
	def(Type{"gift_claim_rejected", CatGames, 0, "/", "⚠️ Your gift request was declined: {reason}. The gift is back in your collection.", "⚠️ Заявка на подарок отклонена: {reason}. Подарок вернулся в коллекцию.", "Open game", "Открыть игру"})
	def(Type{"chest_round_open", CatGames, 24 * time.Hour, "/games/roulette", "🎁 You have unopened chests waiting for you!", "🎁 Вас ждут неоткрытые сундуки!", "Open chests", "Открыть"})

	def(Type{"broadcast", CatNews, 0, "/", "", "", "Open game", "Открыть игру"})

	def(Type{"withdrawal_created", CatPayments, 0, "/withdraw/history", "📝 Withdrawal request of {coins} coins ({ton} TON) is created. It is processed within 48 hours.", "📝 Заявка на вывод {coins} монет ({ton} TON) создана. Она обрабатывается в течение 48 часов.", "History", "История"})
	def(Type{"withdrawal_completed", CatPayments, 0, "/withdraw/history", "✅ Your withdrawal of {ton} TON was sent. Transaction: {tx}", "✅ Вывод {ton} TON отправлен. Транзакция: {tx}", "History", "История"})
	def(Type{"withdrawal_failed", CatPayments, 0, "/withdraw/history", "❌ Your withdrawal could not be completed. {coins} coins were returned to your balance.", "❌ Вывод не удалось выполнить. {coins} монет возвращены на баланс.", "History", "История"})
	def(Type{"withdrawal_cancelled", CatPayments, 0, "/withdraw/history", "↩️ Withdrawal cancelled, {coins} coins were returned.", "↩️ Вывод отменён, {coins} монет возвращены.", "History", "История"})
	def(Type{"topup_paid", CatPayments, 0, "/", "⭐ {stars} stars were added to your balance.", "⭐ {stars} звёзд зачислены на ваш баланс.", "Open game", "Открыть игру"})
	def(Type{"topup_refunded", CatPayments, 0, "/", "↩️ Your payment was refunded, {stars} stars were taken back.", "↩️ Платёж возвращён, {stars} звёзд списаны.", "Open game", "Открыть игру"})
	def(Type{"account_blocked", CatPayments, 0, "/", "🚫 Your account was blocked. Please contact support.", "🚫 Ваш аккаунт заблокирован. Обратитесь в поддержку.", "Open game", "Открыть игру"})
	def(Type{"account_unblocked", CatPayments, 0, "/", "✅ Your account is active again.", "✅ Ваш аккаунт снова активен.", "Open game", "Открыть игру"})
	def(Type{"security_alert", CatPayments, 0, "/", "🔐 {text}", "🔐 {text}", "Open game", "Открыть игру"})
}

// TypeOf returns the definition of a type.
func TypeOf(name string) (Type, bool) { t, ok := types[name]; return t, ok }

// Names lists every registered type (for the admin template editor).
func Names() []string {
	out := make([]string, 0, len(types))
	for k := range types {
		out = append(out, k)
	}
	return out
}

// localize lets one set of parameters carry both languages: "name_en" and "name_ru" become "name" for the reader.
func localize(params map[string]any, lang apperr.Lang) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	suffix := "_en"
	if lang == apperr.RU {
		suffix = "_ru"
	}
	for k, v := range params {
		if strings.HasSuffix(k, suffix) {
			out[strings.TrimSuffix(k, suffix)] = v
		}
	}
	return out
}

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

// fill replaces {name} with the HTML-escaped parameter; a missing parameter stays empty.
func fill(text string, params map[string]any) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		key := strings.Trim(m, "{}")
		if v, ok := params[key]; ok {
			return html.EscapeString(toString(v))
		}
		return ""
	})
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return formatInt(int64(x))
		}
		return strings.TrimRight(strings.TrimRight(formatFloat(x), "0"), ".")
	case int64:
		return formatInt(x)
	case int:
		return formatInt(int64(x))
	default:
		return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(sprint(v), "\n", " "), "\t", " "))
	}
}
