package ads

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"brunoplay/internal/apperr"
	"brunoplay/internal/settings"
)

// Ad providers. Adding a new one means adding one entry to `providers` below (plus its player in the app); the admin Ads page,
// the on/off switches and the priority list work for every entry without more changes.

// Format is a kind of ad a provider can serve.
type Format string

const (
	FormatRewarded     Format = "rewarded"     // the player watches it to get an action or a reward (proof required)
	FormatInterstitial Format = "interstitial" // shown between games, nothing is paid
	FormatTask         Format = "task"         // the sponsored task card
	FormatBanner       Format = "banner"       // a block of ad cards inside a screen (nothing is paid)
)

// Field is one setting the admin edits on the Ads page for a provider.
type Field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Help  string `json:"help"`
	Kind  string `json:"kind"` // "string" | "bool" | "number"
}

// ProviderRef is what the app needs to play an ad of one provider.
type ProviderRef struct {
	ID       string `json:"id"`
	BlockID  string `json:"blockId,omitempty"`  // Adsgram
	WidgetID string `json:"widgetId,omitempty"` // TADS
	ZoneID   string `json:"zoneId,omitempty"`   // Monetag (the app also uses the proof id as the ymid of the ad)
	Debug    bool   `json:"debug,omitempty"`
	// WaitSeconds: the provider reports an ad when it STARTS, not when it ends. The server accepts the proof this many seconds after
	// the report, and the app waits as long before it goes on.
	WaitSeconds int `json:"waitSeconds,omitempty"`
}

type providerDef struct {
	ID        string
	Name      string
	Formats   []Format
	DefaultOn bool
	Fields    []Field
	// WaitKey is the setting with the seconds to wait after the provider's report (empty: the report comes when the ad is over)
	WaitKey string
	// Ref returns what the app needs for a placement, or false when the provider is not set up (no block id, no widget id...).
	Ref func(ctx context.Context, s *Service, placement string) (ProviderRef, bool)
	// Webhooks lists the addresses the admin must give to the provider's dashboard (publicURL has no trailing slash).
	Webhooks func(publicURL string) []Webhook
}

// Webhook is an address for the provider's dashboard. The secret is never shown: the admin adds it from the server's .env.
type Webhook struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Note  string `json:"note"`
}

var providers = []providerDef{
	{
		ID: "adsgram", Name: "Adsgram", Formats: []Format{FormatRewarded, FormatInterstitial, FormatTask}, DefaultOn: true,
		Ref: func(ctx context.Context, s *Service, placement string) (ProviderRef, bool) {
			block, err := s.blockID(ctx, placement)
			block = strings.TrimSpace(block)
			return ProviderRef{ID: "adsgram", BlockID: block}, err == nil && block != ""
		},
		Webhooks: func(u string) []Webhook {
			return []Webhook{
				{Label: "Rewarded blocks (Reward URL)", URL: u + "/api/webhooks/adsgram?userid=[userId]&blockid=[blockId]&secret=YOUR_SECRET", Note: "YOUR_SECRET is ADSGRAM_WEBHOOK_SECRET from the server's .env"},
				{Label: "Task block (Reward URL)", URL: u + "/api/webhooks/adsgram-task?userid=[userId]&secret=YOUR_SECRET", Note: "same secret"},
			}
		},
	},
	{
		ID: "tads", Name: "TADS", Formats: []Format{FormatRewarded, FormatBanner},
		Fields: []Field{
			{Key: settings.KeyAdsTadsWidget, Label: "Widget id", Help: "The number of the Fullscreen banner (rewarded) widget in the TADS dashboard, for example 12941.", Kind: "string"},
			{Key: settings.KeyAdsTadsTGB, Label: "TGB widget id (test card)", Kind: "string",
				Help: "A TGB (text-graphic block) widget of TADS. When filled in, a card with its ads appears on the Tasks screen (it pays nothing). Empty = no card."},
			{Key: settings.KeyAdsTadsWait, Label: "Wait for the ad to end (seconds)", Kind: "number",
				Help: "TADS reports an ad when it STARTS, not when it ends. The game stays on hold this many seconds after the ad starts, and the server refuses the action before that. Watch one TADS ad, time it, and put its length (or a little more) here. 0 = no wait."},
			{Key: settings.KeyAdsTadsDebug, Label: "Debug mode", Help: "Test ads. Keep it OFF in production.", Kind: "bool"},
		},
		WaitKey: settings.KeyAdsTadsWait,
		Ref: func(ctx context.Context, s *Service, placement string) (ProviderRef, bool) {
			w := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsTadsWidget, ""))
			wait, _ := s.settings.Int(ctx, settings.KeyAdsTadsWait)
			return ProviderRef{ID: "tads", WidgetID: w, Debug: s.settings.BoolOr(ctx, settings.KeyAdsTadsDebug, false), WaitSeconds: int(wait)}, widgetRe.MatchString(w)
		},
		Webhooks: func(u string) []Webhook {
			return []Webhook{{Label: "Widget webhook (method POST)", URL: u + "/api/webhooks/tads?secret=YOUR_SECRET", Note: "YOUR_SECRET is ADSGRAM_WEBHOOK_SECRET from the server's .env (the same secret is used)"}}
		},
	},
	{
		ID: "monetag", Name: "Monetag", Formats: []Format{FormatRewarded},
		Fields: []Field{
			{Key: settings.KeyAdsMonetagZone, Label: "Main zone id", Help: "The number of the main SDK zone of the app in the Monetag dashboard (Telegram Mini Apps), for example 12144526.", Kind: "string"},
			{Key: settings.KeyAdsMonetagWait, Label: "Wait for the ad to end (seconds)", Kind: "number",
				Help: "Monetag reports an ad when it STARTS, not when it ends. The game stays on hold this many seconds after the ad starts, and the server refuses the action before that. Put the length of a Monetag ad here. 0 = no wait."},
		},
		WaitKey: settings.KeyAdsMonetagWait,
		Ref: func(ctx context.Context, s *Service, placement string) (ProviderRef, bool) {
			z := strings.TrimSpace(s.settings.StrOr(ctx, settings.KeyAdsMonetagZone, ""))
			wait, _ := s.settings.Int(ctx, settings.KeyAdsMonetagWait)
			return ProviderRef{ID: "monetag", ZoneID: z, WaitSeconds: int(wait)}, widgetRe.MatchString(z)
		},
		Webhooks: func(u string) []Webhook {
			return []Webhook{{Label: "Postback URL (Monetag → the app's Postback page)", URL: u + "/api/webhooks/monetag?secret=YOUR_SECRET&ymid={ymid}&zone_id={zone_id}&event_type={event_type}&reward_event_type={reward_event_type}&telegram_id={telegram_id}", Note: "YOUR_SECRET is ADSGRAM_WEBHOOK_SECRET from the server's .env (the same secret is used). Type the secret yourself, never share it."}}
		},
	},
}

var widgetRe = regexp.MustCompile(`^[0-9]{1,12}$`)

func findProvider(id string) *providerDef {
	for i := range providers {
		if providers[i].ID == id {
			return &providers[i]
		}
	}
	return nil
}

// waitSeconds is how long after the provider's report the proof becomes usable (0 for providers that report at the end of the ad).
func (s *Service) waitSeconds(ctx context.Context, provider string) int64 {
	p := findProvider(provider)
	if p == nil || p.WaitKey == "" {
		return 0
	}
	n, err := s.settings.Int(ctx, p.WaitKey)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (p *providerDef) has(f Format) bool {
	for _, x := range p.Formats {
		if x == f {
			return true
		}
	}
	return false
}

// Entry is one line of the priority list: the order of the list is the priority.
type Entry struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// entries reads the priority list. Unknown ids are dropped and providers that are not in the stored list yet (a newly added one)
// are appended at the end with their default switch.
func (s *Service) entries(ctx context.Context) []Entry {
	stored, _ := settings.Get[[]Entry](ctx, s.settings, settings.KeyAdsProviders)
	out := make([]Entry, 0, len(providers))
	seen := map[string]bool{}
	for _, e := range stored {
		if findProvider(e.ID) == nil || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	for _, p := range providers {
		if !seen[p.ID] {
			out = append(out, Entry{ID: p.ID, Enabled: p.DefaultOn})
		}
	}
	return out
}

// Chain lists, in priority order, the providers that can serve a rewarded ad for the placement right now.
func (s *Service) Chain(ctx context.Context, placement string) []ProviderRef {
	var out []ProviderRef
	for _, e := range s.entries(ctx) {
		p := findProvider(e.ID)
		if !e.Enabled || !p.has(FormatRewarded) {
			continue
		}
		if ref, ok := p.Ref(ctx, s, placement); ok {
			out = append(out, ref)
		}
	}
	return out
}

// ProviderOn reports whether a provider is switched on in the admin and serves the format (interstitial, task).
func (s *Service) ProviderOn(ctx context.Context, id string, f Format) bool {
	p := findProvider(id)
	if p == nil || !p.has(f) {
		return false
	}
	for _, e := range s.entries(ctx) {
		if e.ID == id {
			return e.Enabled
		}
	}
	return false
}

func chainIDs(c []ProviderRef) string {
	ids := make([]string, len(c))
	for i, r := range c {
		ids[i] = r.ID
	}
	return strings.Join(ids, ",")
}

// ---- admin

// FieldView is a provider field with its current value.
type FieldView struct {
	Field
	Value any `json:"value"`
}

// ProviderView is one provider on the admin Ads page.
type ProviderView struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Formats  []Format    `json:"formats"`
	Enabled  bool        `json:"enabled"`
	Ready    bool        `json:"ready"` // set up well enough to serve rewarded ads (or, without that format, to be used at all)
	Problem  string      `json:"problem"`
	Fields   []FieldView `json:"fields"`
	Webhooks []Webhook   `json:"webhooks"`
}

// ProvidersView is the admin Ads page: the providers in priority order.
func (s *Service) ProvidersView(ctx context.Context) []ProviderView {
	out := []ProviderView{}
	for _, e := range s.entries(ctx) {
		p := findProvider(e.ID)
		v := ProviderView{ID: p.ID, Name: p.Name, Formats: p.Formats, Enabled: e.Enabled, Fields: []FieldView{}, Webhooks: []Webhook{}}
		if p.Webhooks != nil {
			v.Webhooks = p.Webhooks(strings.TrimRight(s.publicURL, "/"))
		}
		for _, f := range p.Fields {
			fv := FieldView{Field: f}
			switch f.Kind {
			case "bool":
				fv.Value = s.settings.BoolOr(ctx, f.Key, false)
			case "number":
				n, _ := s.settings.Int(ctx, f.Key)
				fv.Value = n
			default:
				fv.Value = s.settings.StrOr(ctx, f.Key, "")
			}
			v.Fields = append(v.Fields, fv)
		}
		// "ready" is judged on the placement every provider shares (fishing start)
		if _, ok := p.Ref(ctx, s, FishingStart); ok {
			v.Ready = true
		} else {
			v.Problem = "Not set up yet: " + providerHint(p.ID)
		}
		out = append(out, v)
	}
	return out
}

func providerHint(id string) string {
	switch id {
	case "adsgram":
		return "enter the Adsgram block ids in Settings → ads.block_ids."
	case "tads":
		return "enter the widget id below."
	case "monetag":
		return "enter the main zone id below."
	}
	return "fill in its settings."
}

// SaveProviders stores the priority list and the provider fields. fields may only name keys that belong to a provider.
func (s *Service) SaveProviders(ctx context.Context, list []Entry, fields map[string]any, adminID int64) error {
	seen := map[string]bool{}
	for _, e := range list {
		if findProvider(e.ID) == nil {
			return apperr.AdminRefused.With("why", "Unknown ad provider: "+e.ID)
		}
		if seen[e.ID] {
			return apperr.AdminRefused.With("why", "The provider "+e.ID+" is in the list twice")
		}
		seen[e.ID] = true
	}
	for _, p := range providers {
		if !seen[p.ID] {
			return apperr.AdminRefused.With("why", "The list must contain every provider (missing: "+p.Name+")")
		}
	}
	allowed := map[string]Field{}
	for _, p := range providers {
		for _, f := range p.Fields {
			allowed[f.Key] = f
		}
	}
	for k, v := range fields {
		f, ok := allowed[k]
		if !ok {
			return apperr.AdminRefused.With("why", "Unknown provider setting: "+k)
		}
		if f.Kind == "string" {
			str, isStr := v.(string)
			if !isStr {
				return apperr.AdminRefused.With("why", f.Label+" must be text")
			}
			if (k == settings.KeyAdsTadsWidget || k == settings.KeyAdsTadsTGB || k == settings.KeyAdsMonetagZone) && str != "" && !widgetRe.MatchString(strings.TrimSpace(str)) {
				return apperr.AdminRefused.With("why", f.Label+" is a number (for example 12941)")
			}
			fields[k] = strings.TrimSpace(str)
		}
		if f.Kind == "number" {
			if n, isNum := v.(float64); !isNum || n != float64(int64(n)) {
				return apperr.AdminRefused.With("why", f.Label+" must be a whole number")
			}
		}
	}
	for k, v := range fields {
		if err := s.settings.Set(ctx, k, v, adminID); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(list)
	var asAny any
	_ = json.Unmarshal(raw, &asAny)
	return s.settings.Set(ctx, settings.KeyAdsProviders, asAny, adminID)
}
