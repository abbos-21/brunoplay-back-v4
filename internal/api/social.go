package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"brunoplay/internal/admin"
	"brunoplay/internal/ads"
	"brunoplay/internal/apperr"
	"brunoplay/internal/httpx"
	"brunoplay/internal/tasks"
)

// dailyRoutes: streak, combo, tasks, bonus ad, friends (milestone 4).
func (d *Deps) socialRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(d.MW.PlayerAuth(false))
		r.Get("/daily/streak", httpx.Wrap(d.streakStatus))
		r.Get("/daily/combo", httpx.Wrap(d.comboStatus))
		r.Get("/tasks", httpx.Wrap(d.tasksList))
		r.Get("/ads/bonus", httpx.Wrap(d.bonusStatus))
		r.Get("/ads/config", httpx.Wrap(d.adsConfig))
		r.Get("/friends", httpx.Wrap(d.friends))
		r.Get("/friends/stats", httpx.Wrap(d.friendsStats))
		r.Get("/friends/rewards", httpx.Wrap(d.friendsRewards))
		r.Get("/friends/link", httpx.Wrap(d.friendsLink))
		r.Post("/tasks/seen", httpx.Wrap(d.tasksSeen))
		r.Post("/tasks/{id}/open", httpx.Wrap(d.taskOpen))

		r.Group(func(r chi.Router) {
			r.Use(d.MW.Limit(d.MoneyLimit, "money"), d.MW.Idempotency)
			r.Post("/daily/streak/claim", httpx.Wrap(d.streakClaim))
			r.Post("/daily/combo/submit", httpx.Wrap(d.comboSubmit))
			r.Post("/daily/combo/claim", httpx.Wrap(d.comboClaim))
			r.Post("/tasks/{id}/check", httpx.Wrap(d.taskCheck))
			r.Post("/tasks/{id}/ad-progress", httpx.Wrap(d.taskAdProgress))
			r.Post("/ads/bonus/claim", httpx.Wrap(d.bonusClaim))
			r.Post("/ads/task/claim", httpx.Wrap(d.sponsoredClaim))
		})
	})
}

// placementGuards refuse an ad request that could not be used afterwards.
func (d *Deps) placementGuards(r *http.Request, playerID int64, placement string) error {
	ctx := r.Context()
	switch placement {
	case ads.ComboExtra:
		return d.Daily.CanExtraAttempt(ctx, playerID)
	case ads.DoubleStreak, ads.DoubleCombo:
		return d.Daily.CanClaimDouble(ctx, playerID, placement)
	case ads.RideFree:
		info, err := d.Games.RideInfo(ctx, playerID)
		if err != nil {
			return err
		}
		if !info.AdOffered {
			return apperr.AdLimit
		}
	case ads.BonusCoins:
		st, err := d.Ads.Bonus(ctx, playerID)
		if err != nil {
			return err
		}
		if st.Left <= 0 {
			return apperr.AdLimit
		}
	}
	return nil
}

// ---- streak and combo

func (d *Deps) streakStatus(w http.ResponseWriter, r *http.Request) error {
	st, err := d.Daily.Streak(r.Context(), httpx.PlayerFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

type claimReq struct {
	Double   bool   `json:"double"`
	AdViewID string `json:"adViewId" validate:"omitempty,max=64"`
}

func (d *Deps) streakClaim(w http.ResponseWriter, r *http.Request) error {
	var in claimReq
	if err := httpx.DecodeOptional(r, &in); err != nil {
		return err
	}
	res, err := d.Daily.ClaimStreak(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.Double, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) comboStatus(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	st, err := d.Daily.Combo(r.Context(), p.ID, p.Lang())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

type comboReq struct {
	Items    []int16 `json:"items" validate:"required,len=4"`
	AdViewID string  `json:"adViewId" validate:"omitempty,max=64"`
}

func (d *Deps) comboSubmit(w http.ResponseWriter, r *http.Request) error {
	var in comboReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := d.Daily.SubmitCombo(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.Items, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) comboClaim(w http.ResponseWriter, r *http.Request) error {
	var in claimReq
	if err := httpx.DecodeOptional(r, &in); err != nil {
		return err
	}
	res, err := d.Daily.ClaimCombo(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.Double, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

// ---- tasks and the bonus ad

func (d *Deps) tasksList(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	list, newCount, err := d.Tasks.List(r.Context(), p)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"tasks": list, "newCount": newCount})
	return nil
}

func (d *Deps) tasksSeen(w http.ResponseWriter, r *http.Request) error {
	if err := d.Tasks.MarkSeen(r.Context(), httpx.PlayerFrom(r.Context()).ID); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) taskOpen(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := d.Tasks.Open(r.Context(), httpx.PlayerFrom(r.Context()).ID, id); err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) taskCheck(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	res, err := d.Tasks.Check(r.Context(), httpx.PlayerFrom(r.Context()), id)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) taskAdProgress(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in adReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := d.Tasks.AdProgress(r.Context(), httpx.PlayerFrom(r.Context()), id, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

func (d *Deps) bonusStatus(w http.ResponseWriter, r *http.Request) error {
	st, err := d.Ads.Bonus(r.Context(), httpx.PlayerFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

func (d *Deps) bonusClaim(w http.ResponseWriter, r *http.Request) error {
	var in adReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := d.Ads.ClaimBonus(r.Context(), httpx.PlayerFrom(r.Context()).ID, in.AdViewID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}

// ---- friends

func (d *Deps) friends(w http.ResponseWriter, r *http.Request) error {
	p := httpx.PlayerFrom(r.Context())
	st, err := d.Referrals.Stats(r.Context(), p.ID)
	if err != nil {
		return err
	}
	page := int(queryInt(r, "page", 1))
	list, total, err := d.Referrals.Friends(r.Context(), p.ID, page, 20)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"stats": st, "friends": list, "total": total, "page": page, "rewards": d.Referrals.Rewards(r.Context()),
		"links": d.Referrals.Links(p.ID)})
	return nil
}

func (d *Deps) friendsStats(w http.ResponseWriter, r *http.Request) error {
	st, err := d.Referrals.Stats(r.Context(), httpx.PlayerFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, st)
	return nil
}

func (d *Deps) friendsRewards(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, map[string]any{"tiers": d.Referrals.Rewards(r.Context())})
	return nil
}

func (d *Deps) friendsLink(w http.ResponseWriter, r *http.Request) error {
	httpx.JSON(w, 200, d.Referrals.Links(httpx.PlayerFrom(r.Context()).ID))
	return nil
}

// ---- admin: tasks, combo, referrals

func (d *Deps) socialAdminRoutes(r chi.Router) {
	r.With(d.adminAuth("tasks.read")).Get("/tasks", httpx.Wrap(d.adminTasks))
	r.With(d.adminAuth("tasks.write")).Post("/tasks", httpx.Wrap(d.adminTaskCreate))
	r.With(d.adminAuth("tasks.write")).Put("/tasks/{id}", httpx.Wrap(d.adminTaskUpdate))
	r.With(d.adminAuth("tasks.write")).Delete("/tasks/{id}", httpx.Wrap(d.adminTaskDelete))

	r.With(d.adminAuth("combo.read")).Get("/combo/images", httpx.Wrap(d.adminComboImages))
	r.With(d.adminAuth("combo.write")).Patch("/combo/images/{id}", httpx.Wrap(d.adminComboImageUpdate))
	r.With(d.adminAuth("combo.read")).Get("/combo/patterns", httpx.Wrap(d.adminComboPatterns))
	r.With(d.adminAuth("combo.write")).Put("/combo/patterns/{day}", httpx.Wrap(d.adminComboPatternSet))
	r.With(d.adminAuth("combo.write")).Delete("/combo/patterns/{day}", httpx.Wrap(d.adminComboPatternDelete))
}

func (d *Deps) adminTasks(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Tasks.AdminList(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"tasks": l})
	return nil
}

func (d *Deps) adminTaskCreate(w http.ResponseWriter, r *http.Request) error {
	var in tasks.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t, err := d.Tasks.Create(r.Context(), in)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "task.create", "task", itoa(t.ID), nil, t)
	if t.Active && (t.StartsAt == nil || !t.StartsAt.After(d.now())) {
		// everybody who can be reached hears about a new task (once per task)
		_, _ = d.Notify.BulkAll(r.Context(), "task_new", nil, fmt.Sprintf("tasknew:%d:", t.ID))
	}
	httpx.JSON(w, 201, t)
	return nil
}

func (d *Deps) adminTaskUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in tasks.Input
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t, err := d.Tasks.Update(r.Context(), id, in)
	if err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "task.update", "task", itoa(id), nil, t)
	httpx.JSON(w, 200, t)
	return nil
}

func (d *Deps) adminTaskDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := d.Tasks.Delete(r.Context(), id); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "task.delete", "task", itoa(id), nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminComboImages(w http.ResponseWriter, r *http.Request) error {
	l, err := d.Daily.AllImages(r.Context())
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"images": l})
	return nil
}

type imageReq struct {
	NameEN *string `json:"nameEn" validate:"omitempty,max=60"`
	NameRU *string `json:"nameRu" validate:"omitempty,max=60"`
	Icon   *string `json:"icon" validate:"omitempty,max=500"`
	Active *bool   `json:"active"`
}

func (d *Deps) adminComboImageUpdate(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var in imageReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := d.Daily.UpdateImage(r.Context(), int(id), in.NameEN, in.NameRU, in.Icon, in.Active); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "combo.image", "combo_image", itoa(id), nil, in)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func parseDay(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, apperr.Validation.With("field", "day")
	}
	return t, nil
}

func (d *Deps) adminComboPatterns(w http.ResponseWriter, r *http.Request) error {
	from, to := d.now().AddDate(0, 0, -7), d.now().AddDate(0, 0, 30)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := parseDay(v)
		if err != nil {
			return err
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := parseDay(v)
		if err != nil {
			return err
		}
		to = t
	}
	l, err := d.Daily.Patterns(r.Context(), from, to)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, map[string]any{"patterns": l})
	return nil
}

type patternReq struct {
	Items []int16 `json:"items" validate:"required,len=4"`
}

func (d *Deps) adminComboPatternSet(w http.ResponseWriter, r *http.Request) error {
	day, err := parseDay(chi.URLParam(r, "day"))
	if err != nil {
		return err
	}
	var in patternReq
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	who := httpx.AdminFrom(r.Context())
	if err := d.Daily.SetPattern(r.Context(), day, in.Items, who.ID); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, who, "combo.pattern", "combo_pattern", day.Format("2006-01-02"), nil, in)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

func (d *Deps) adminComboPatternDelete(w http.ResponseWriter, r *http.Request) error {
	day, err := parseDay(chi.URLParam(r, "day"))
	if err != nil {
		return err
	}
	if err := d.Daily.DeletePattern(r.Context(), day); err != nil {
		return err
	}
	_ = admin.Audit(r.Context(), d.Pool, httpx.AdminFrom(r.Context()), "combo.pattern_delete", "combo_pattern", day.Format("2006-01-02"), nil, nil)
	httpx.JSON(w, 200, map[string]bool{"ok": true})
	return nil
}

// adsConfig tells the game how to run the Interstitial ads and whether to show the sponsored task.
func (d *Deps) adsConfig(w http.ResponseWriter, r *http.Request) error {
	cfg, err := d.Ads.Config(r.Context(), httpx.PlayerFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, cfg)
	return nil
}

func (d *Deps) sponsoredClaim(w http.ResponseWriter, r *http.Request) error {
	res, err := d.Ads.ClaimSponsored(r.Context(), httpx.PlayerFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.JSON(w, 200, res)
	return nil
}
