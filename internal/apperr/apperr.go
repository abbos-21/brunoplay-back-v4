// Package apperr defines the stable machine error codes of the API and their RU/EN texts.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type Lang string

const (
	EN Lang = "EN"
	RU Lang = "RU"
)

func ParseLang(s string) Lang {
	if strings.EqualFold(strings.TrimSpace(s), "ru") || strings.HasPrefix(strings.ToLower(s), "ru") {
		return RU
	}
	return EN
}

// Error is an API error: a stable code, an HTTP status and optional parameters for the text.
type Error struct {
	Code   string
	Status int
	Params map[string]any
	cause  error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.cause.Error()
	}
	return e.Code
}
func (e *Error) Unwrap() error { return e.cause }

// With adds text parameters, e.g. {min} in "Minimum is {min}".
func (e *Error) With(k string, v any) *Error {
	c := *e
	c.Params = map[string]any{}
	for kk, vv := range e.Params {
		c.Params[kk] = vv
	}
	c.Params[k] = v
	return &c
}

// Wrap keeps an internal cause for logs; the client never sees it.
func (e *Error) Wrap(cause error) *Error {
	c := *e
	c.cause = cause
	return &c
}

func (e *Error) Message(l Lang) string {
	m, ok := messages[e.Code]
	if !ok {
		m = messages["INTERNAL"]
	}
	text := m.en
	if l == RU {
		text = m.ru
	}
	for k, v := range e.Params {
		text = strings.ReplaceAll(text, "{"+k+"}", fmt.Sprint(v))
	}
	return text
}

type msg struct{ en, ru string }

var messages = map[string]msg{}

// def registers a code with its texts and returns the Error value.
func def(code string, status int, en, ru string) *Error {
	messages[code] = msg{en, ru}
	return &Error{Code: code, Status: status}
}

// As extracts an *Error from err (or nil).
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

func Is(err error, code *Error) bool {
	e := As(err)
	return e != nil && e.Code == code.Code
}

var (
	// generic
	Internal       = def("INTERNAL", http.StatusInternalServerError, "Something went wrong, please try again", "Что-то пошло не так, попробуйте ещё раз")
	BadRequest     = def("BAD_REQUEST", http.StatusBadRequest, "The request is not valid", "Некорректный запрос")
	Validation     = def("VALIDATION", http.StatusUnprocessableEntity, "Please check the entered values", "Проверьте введённые значения")
	NotFound       = def("NOT_FOUND", http.StatusNotFound, "Not found", "Не найдено")
	Unauthorized   = def("UNAUTHORIZED", http.StatusUnauthorized, "Please sign in again", "Войдите заново")
	Forbidden      = def("FORBIDDEN", http.StatusForbidden, "You do not have access to this", "Нет доступа")
	TooManyReq     = def("RATE_LIMITED", http.StatusTooManyRequests, "Too many requests, slow down", "Слишком много запросов, подождите")
	Maintenance    = def("MAINTENANCE", http.StatusServiceUnavailable, "We are updating the game, back soon", "Идёт обновление игры, скоро вернёмся")
	UpgradeNeeded  = def("UPGRADE_REQUIRED", http.StatusUpgradeRequired, "Please reload the app to update", "Перезапустите приложение для обновления")
	ConfigMissing  = def("CONFIG_MISSING", http.StatusServiceUnavailable, "This feature is not available yet", "Эта функция пока недоступна")
	AccountBlocked = def("ACCOUNT_BLOCKED", http.StatusForbidden, "Your account is blocked", "Ваш аккаунт заблокирован")
	UnderReview    = def("UNDER_REVIEW", http.StatusForbidden, "This action is under review", "Это действие на проверке")
	IdemInProgress = def("IDEMPOTENCY_IN_PROGRESS", http.StatusConflict, "This request is already being processed", "Этот запрос уже обрабатывается")

	// auth
	AuthFailed  = def("AUTH_FAILED", http.StatusUnauthorized, "Authentication failed", "Не удалось войти")
	AuthExpired = def("AUTH_EXPIRED", http.StatusUnauthorized, "The session expired, reopen the app", "Сессия истекла, откройте приложение заново")

	// money
	InsufficientBalance = def("INSUFFICIENT_BALANCE", http.StatusConflict, "Not enough balance", "Недостаточно средств")
	InsufficientCoins   = def("INSUFFICIENT_COINS", http.StatusConflict, "Not enough coins", "Недостаточно монет")
	InsufficientStars   = def("INSUFFICIENT_STARS", http.StatusConflict, "Not enough stars", "Недостаточно звёзд")

	// ads
	AdNotCompleted = def("AD_NOT_COMPLETED", http.StatusConflict, "Watch the ad to continue", "Посмотрите рекламу, чтобы продолжить")
	AdExpired      = def("AD_EXPIRED", http.StatusConflict, "The ad expired, watch it again", "Реклама устарела, посмотрите ещё раз")
	AdUsed         = def("AD_ALREADY_USED", http.StatusConflict, "This ad was already used", "Эта реклама уже использована")
	AdLimit        = def("AD_LIMIT_REACHED", http.StatusConflict, "Daily limit reached, come back tomorrow", "Дневной лимит исчерпан, возвращайтесь завтра")
	AdDisabled     = def("AD_PLACEMENT_DISABLED", http.StatusConflict, "This option is switched off", "Эта опция отключена")
	AdTaskWait     = def("AD_TASK_NOT_READY", http.StatusConflict, "The next sponsored task is not available yet", "Следующее спонсорское задание пока недоступно")

	// fishing
	SessionRunning  = def("SESSION_ALREADY_RUNNING", http.StatusConflict, "Fishing is already running", "Рыбалка уже идёт")
	SessionNotRun   = def("SESSION_NOT_RUNNING", http.StatusConflict, "Fishing has not started", "Рыбалка не начата")
	TankBelowMin    = def("TANK_BELOW_MINIMUM", http.StatusConflict, "The tank must be at least {percent}% full", "Садок должен быть заполнен минимум на {percent}%")
	CardMaxLevel    = def("CARD_MAX_LEVEL", http.StatusConflict, "This card is at the maximum level", "Эта карточка уже на максимальном уровне")
	ReqNotMet       = def("REQUIREMENT_NOT_MET", http.StatusConflict, "Requirements are not met", "Условия не выполнены")
	BoosterActive   = def("BOOSTER_ALREADY_ACTIVE", http.StatusConflict, "This booster is already active", "Этот бустер уже активен")
	BoosterNotFound = def("BOOSTER_NOT_FOUND", http.StatusNotFound, "Booster not found", "Бустер не найден")

	// top-up / payments
	TopupPackInvalid = def("TOPUP_PACK_INVALID", http.StatusUnprocessableEntity, "This pack is not available", "Этот пакет недоступен")
	PaymentPending   = def("PAYMENT_PENDING", http.StatusConflict, "The payment is still being confirmed", "Платёж ещё подтверждается")

	// daily
	StreakClaimed  = def("STREAK_ALREADY_CLAIMED", http.StatusConflict, "You already claimed today's reward", "Вы уже забрали сегодняшнюю награду")
	ComboAttempted = def("COMBO_ALREADY_ATTEMPTED", http.StatusConflict, "You already played today's combo", "Вы уже играли в комбо сегодня")
	ComboInvalid   = def("COMBO_INVALID_ITEMS", http.StatusUnprocessableEntity, "Choose four different pictures", "Выберите четыре разные картинки")
	ComboNothing   = def("COMBO_NOTHING_TO_CLAIM", http.StatusConflict, "There is nothing to claim", "Нечего забирать")
	ComboClaimed   = def("COMBO_ALREADY_CLAIMED", http.StatusConflict, "The combo reward was already claimed", "Награда за комбо уже получена")
	ExtraLimit     = def("EXTRA_LIMIT_REACHED", http.StatusConflict, "No more extra attempts today", "Дополнительные попытки на сегодня закончились")

	// games
	ChestRoundOpen = def("CHEST_ROUND_OPEN", http.StatusConflict, "Finish your current chest round first", "Сначала завершите текущий раунд")
	ChestNoRound   = def("CHEST_NO_ROUND", http.StatusConflict, "There is no active chest round", "Нет активного раунда сундуков")
	RideRunInvalid = def("RIDE_RUN_INVALID", http.StatusConflict, "This run cannot be saved", "Этот заезд нельзя сохранить")
	GiftNotOwned   = def("GIFT_NOT_AVAILABLE", http.StatusConflict, "This gift is not available", "Этот подарок недоступен")
	AdminRefused   = def("ADMIN_ACTION_REFUSED", http.StatusConflict, "{why}", "{why}")            // an admin action that cannot be done now; the text says why
	GiftCatalog    = def("GIFT_CATALOG_INVALID", http.StatusUnprocessableEntity, "{why}", "{why}") // an admin mistake; the text says what to fix
	SpinInvalid    = def("SPIN_INVALID", http.StatusUnprocessableEntity, "Choose a valid spin", "Выберите допустимое вращение")

	// tournaments
	TournamentClosed = def("TOURNAMENT_NOT_OPEN", http.StatusConflict, "This tournament is not open", "Турнир недоступен")
	TournamentJoined = def("TOURNAMENT_ALREADY_JOINED", http.StatusConflict, "You already joined", "Вы уже участвуете")

	// tasks / friends
	TaskDone         = def("TASK_ALREADY_COMPLETED", http.StatusConflict, "You already completed this task", "Вы уже выполнили это задание")
	TaskCannotVerify = def("TASK_CANNOT_VERIFY", http.StatusServiceUnavailable, "We cannot verify this task right now, try again later", "Сейчас не удаётся проверить задание, попробуйте позже")
	TaskNotDone      = def("TASK_NOT_COMPLETED", http.StatusConflict, "The task is not completed yet", "Задание ещё не выполнено")

	// withdrawals
	WdOpen         = def("WITHDRAWAL_ALREADY_OPEN", http.StatusConflict, "You already have a withdrawal in progress", "У вас уже есть заявка на вывод")
	WdCooldown     = def("WITHDRAWAL_COOLDOWN", http.StatusConflict, "Please wait before the next withdrawal", "Подождите перед следующим выводом")
	WdMin          = def("WITHDRAWAL_BELOW_MIN", http.StatusUnprocessableEntity, "Minimum withdrawal is {min} coins", "Минимальный вывод — {min} монет")
	WdMax          = def("WITHDRAWAL_ABOVE_MAX", http.StatusUnprocessableEntity, "Maximum withdrawal is {max} coins", "Максимальный вывод — {max} монет")
	WdLimit        = def("WITHDRAWAL_LIMIT", http.StatusConflict, "Withdrawal limit reached", "Достигнут лимит вывода")
	WdLevel        = def("WITHDRAWAL_LEVEL_TOO_LOW", http.StatusConflict, "Reach level {level} to withdraw", "Достигните уровня {level}, чтобы вывести")
	WdAccountAge   = def("WITHDRAWAL_ACCOUNT_TOO_NEW", http.StatusConflict, "Your account is too new to withdraw", "Аккаунт слишком новый для вывода")
	WdTooSmall     = def("WITHDRAWAL_TOO_SMALL", http.StatusUnprocessableEntity, "The amount is too small to cover the network fee", "Сумма слишком мала, чтобы покрыть комиссию сети")
	TonAddrInvalid = def("TON_ADDRESS_INVALID", http.StatusUnprocessableEntity, "Enter a valid TON address", "Введите корректный TON-адрес")
	WdNotCancel    = def("WITHDRAWAL_NOT_CANCELLABLE", http.StatusConflict, "This withdrawal can no longer be cancelled", "Эту заявку уже нельзя отменить")
)

// Code exposes the registered codes (used by tests to check that every code has RU and EN text).
func Codes() []string {
	out := make([]string, 0, len(messages))
	for k := range messages {
		out = append(out, k)
	}
	return out
}

func HasText(code string) (en, ru string, ok bool) {
	m, ok := messages[code]
	return m.en, m.ru, ok
}
