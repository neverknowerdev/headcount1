package endpoints

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
)

type modelGroupMemberReq struct {
	ProviderID int32  `json:"provider_id"`
	Model      string `json:"model"`
	AllModels  bool   `json:"all_models"`
	IsFree     bool   `json:"is_free"`
}

type modelGroupReq struct {
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Members     []modelGroupMemberReq `json:"members"`
}

var slugCleanRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	s := slugCleanRe.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(s, "-")
}

func (api *API) ListModelGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := api.q.ListModelGroupsForUser(r.Context(), api.currentUserID(r))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, groups)
}

func (api *API) CreateModelGroup(w http.ResponseWriter, r *http.Request) {
	var req modelGroupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		api.respondError(w, http.StatusBadRequest, "Name is required")
		return
	}
	slug := slugify(req.Name)
	if slug == "" {
		api.respondError(w, http.StatusBadRequest, "Name must contain letters or digits")
		return
	}

	uid := api.currentUserID(r)
	group, err := api.q.CreateModelGroup(r.Context(), db.ModelGroup{
		Name:        req.Name,
		Slug:        slug,
		UserID:      &uid,
		Description: req.Description,
	})
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := api.replaceGroupMembers(r, group.ID, req.Members); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	group, _ = api.q.GetModelGroup(r.Context(), group.ID)
	api.respondJSON(w, http.StatusCreated, group)
}

func (api *API) UpdateModelGroup(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req modelGroupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}

	group, err := api.q.GetModelGroup(r.Context(), int32(id))
	if err != nil || !ownedByUser(r, group.UserID) {
		api.respondError(w, http.StatusNotFound, "model group not found")
		return
	}
	if strings.TrimSpace(req.Name) != "" {
		group.Name = req.Name
	}
	group.Description = req.Description
	if _, err := api.q.UpdateModelGroup(r.Context(), group); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := api.replaceGroupMembers(r, group.ID, req.Members); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	group, _ = api.q.GetModelGroup(r.Context(), group.ID)
	api.respondJSON(w, http.StatusOK, group)
}

func (api *API) DeleteModelGroup(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if group, err := api.q.GetModelGroup(r.Context(), int32(id)); err != nil || !ownedByUser(r, group.UserID) {
		api.respondError(w, http.StatusNotFound, "model group not found")
		return
	}
	if err := api.q.DeleteModelGroup(r.Context(), int32(id)); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) replaceGroupMembers(r *http.Request, groupID int32, reqMembers []modelGroupMemberReq) error {
	members := make([]db.ModelGroupMember, 0, len(reqMembers))
	for _, m := range reqMembers {
		if m.ProviderID == 0 {
			continue
		}
		if m.AllModels {
			members = append(members, db.ModelGroupMember{ProviderID: m.ProviderID, AllModels: true, IsFree: m.IsFree})
			continue
		}
		if strings.TrimSpace(m.Model) == "" {
			continue
		}
		members = append(members, db.ModelGroupMember{
			ProviderID: m.ProviderID,
			Model:      strings.TrimSpace(m.Model),
			IsFree:     m.IsFree,
		})
	}
	return api.q.ReplaceModelGroupMembers(r.Context(), groupID, members)
}

// memberStatWindow aggregates stats for one provider+model over one window.
type memberStatWindow struct {
	Requests     int     `json:"requests"`
	Failures     int     `json:"failures"`
	RateLimited  int     `json:"rate_limited"`
	FailureRate  float64 `json:"failure_rate"` // failures / (requests - rate_limited)
	AvgTokPerSec float64 `json:"avg_tokens_per_sec"`
}

type memberStats struct {
	ProviderID     int32            `json:"provider_id"`
	ProviderName   string           `json:"provider_name"`
	Model          string           `json:"model"`
	AllModels      bool             `json:"all_models"`
	IsFree         bool             `json:"is_free"`
	Window5h       memberStatWindow `json:"window_5h"`
	Window3d       memberStatWindow `json:"window_3d"`
	RateLimitedNow bool             `json:"rate_limited_now"`
	CooldownUntil  *time.Time       `json:"cooldown_until"`
}

type statsBucket struct {
	Start       time.Time `json:"start"`
	Success     int       `json:"success"`
	Failures    int       `json:"failures"`
	RateLimited int       `json:"rate_limited"`
}

// GetModelGroupStats returns per-member aggregates over 5h/3d windows plus
// an hourly time series over the last 3 days for the charts. Bucketing is
// done in Go so the query stays portable across SQLite and Postgres.
func (api *API) GetModelGroupStats(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	group, err := api.q.GetModelGroup(r.Context(), int32(id))
	if err != nil || !ownedByUser(r, group.UserID) {
		api.respondError(w, http.StatusNotFound, "model group not found")
		return
	}

	now := time.Now()
	since3d := now.Add(-72 * time.Hour)
	since5h := now.Add(-5 * time.Hour)
	groupID := int32(id)
	stats, err := api.q.ListModelRequestStatsSince(r.Context(), &groupID, since3d)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type key struct {
		providerID int32
		model      string
	}
	agg5h := map[key]*memberStatWindow{}
	agg3d := map[key]*memberStatWindow{}
	tps3dSum := map[key]float64{}
	tps3dN := map[key]int{}
	tps5hSum := map[key]float64{}
	tps5hN := map[key]int{}
	type rateLimitMark struct {
		at    time.Time
		until time.Time
	}
	lastRateLimit := map[key]rateLimitMark{}
	lastSuccess := map[key]time.Time{}

	bucketStart := since3d.Truncate(time.Hour)
	nBuckets := int(now.Sub(bucketStart)/time.Hour) + 1
	buckets := make([]statsBucket, nBuckets)
	for i := range buckets {
		buckets[i].Start = bucketStart.Add(time.Duration(i) * time.Hour)
	}

	addTo := func(m map[key]*memberStatWindow, k key, s db.ModelRequestStat) {
		w, ok := m[k]
		if !ok {
			w = &memberStatWindow{}
			m[k] = w
		}
		w.Requests++
		if s.RateLimited {
			w.RateLimited++
		} else if !s.Success {
			w.Failures++
		}
	}

	// An "any model" member has no fixed model string, so it's also
	// aggregated under a provider-level key (model == "") spanning every
	// concrete model actually invoked for that provider within this group.
	for _, s := range stats {
		keys := []key{{s.ProviderID, s.Model}, {s.ProviderID, ""}}
		for _, k := range keys {
			addTo(agg3d, k, s)
			if s.Success && s.TokensPerSec > 0 {
				tps3dSum[k] += s.TokensPerSec
				tps3dN[k]++
			}
			if !s.CreatedAt.Before(since5h) {
				addTo(agg5h, k, s)
				if s.Success && s.TokensPerSec > 0 {
					tps5hSum[k] += s.TokensPerSec
					tps5hN[k]++
				}
			}
			if s.RateLimited && s.CooldownUntil != nil {
				lastRateLimit[k] = rateLimitMark{at: s.CreatedAt, until: *s.CooldownUntil}
			}
			if s.Success {
				lastSuccess[k] = s.CreatedAt
			}
		}

		idx := int(s.CreatedAt.Sub(bucketStart) / time.Hour)
		if idx >= 0 && idx < nBuckets {
			if s.RateLimited {
				buckets[idx].RateLimited++
			} else if s.Success {
				buckets[idx].Success++
			} else {
				buckets[idx].Failures++
			}
		}
	}

	finalize := func(w *memberStatWindow, tpsSum float64, tpsN int) memberStatWindow {
		if w == nil {
			return memberStatWindow{}
		}
		effective := w.Requests - w.RateLimited
		if effective > 0 {
			w.FailureRate = float64(w.Failures) / float64(effective)
		}
		if tpsN > 0 {
			w.AvgTokPerSec = tpsSum / float64(tpsN)
		}
		return *w
	}

	members := make([]memberStats, 0, len(group.Members))
	for _, m := range group.Members {
		k := key{m.ProviderID, m.Model}
		if m.AllModels {
			k = key{m.ProviderID, ""}
		}
		ms := memberStats{
			ProviderID:   m.ProviderID,
			ProviderName: m.Provider.Name,
			Model:        m.Model,
			AllModels:    m.AllModels,
			IsFree:       m.IsFree,
			Window5h:     finalize(agg5h[k], tps5hSum[k], tps5hN[k]),
			Window3d:     finalize(agg3d[k], tps3dSum[k], tps3dN[k]),
		}
		// Currently rate limited when the latest cooldown is still in the
		// future and no success has landed since the rate limit was recorded.
		if rl, ok := lastRateLimit[k]; ok && rl.until.After(now) {
			if ls, ok := lastSuccess[k]; !ok || ls.Before(rl.at) {
				ms.RateLimitedNow = true
				until := rl.until
				ms.CooldownUntil = &until
			}
		}
		members = append(members, ms)
	}

	api.respondJSON(w, http.StatusOK, map[string]interface{}{
		"group_id": group.ID,
		"slug":     group.Slug,
		"members":  members,
		"buckets":  buckets,
	})
}
