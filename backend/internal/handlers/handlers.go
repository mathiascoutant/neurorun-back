package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"runapp/internal/auth"
	"runapp/internal/config"
	"runapp/internal/models"
	oai "runapp/internal/openai"
	"runapp/internal/store"
	"runapp/internal/strava"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Handlers struct {
	cfg    *config.Config
	db     *store.DB
	strava *strava.Client
	openai *oai.Client

	offerMu     sync.RWMutex
	offerCache  *models.OfferConfig
	offerExpiry time.Time
}

func New(cfg *config.Config, db *store.DB) *Handlers {
	return &Handlers{
		cfg:    cfg,
		db:     db,
		strava: strava.New(cfg.StravaClientID, cfg.StravaClientSecret),
		openai: oai.New(cfg.OpenAIAPIKey, cfg.OpenAIModel),
	}
}

type regBody struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	BirthDate string `json:"birth_date"`
	Gender    string `json:"gender"`
}

func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	var b regBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	b.Email = strings.TrimSpace(strings.ToLower(b.Email))
	b.FirstName = strings.TrimSpace(b.FirstName)
	b.LastName = strings.TrimSpace(b.LastName)
	b.BirthDate = strings.TrimSpace(b.BirthDate)
	if b.Email == "" || len(b.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email et mot de passe (8+ caractères) requis"})
		return
	}
	if b.FirstName == "" || b.LastName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prénom et nom requis"})
		return
	}
	if utf8.RuneCountInString(b.FirstName) > 80 || utf8.RuneCountInString(b.LastName) > 80 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prénom ou nom trop long"})
		return
	}
	if b.BirthDate == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date de naissance requise"})
		return
	}
	bd, err := time.Parse("2006-01-02", b.BirthDate)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date de naissance invalide (AAAA-MM-JJ)"})
		return
	}
	bd = time.Date(bd.Year(), bd.Month(), bd.Day(), 0, 0, 0, 0, time.UTC)
	today := time.Now().UTC()
	todayDay := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if bd.After(todayDay) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date de naissance dans le futur"})
		return
	}
	if bd.Before(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date de naissance invalide"})
		return
	}
	b.BirthDate = bd.Format("2006-01-02")

	g := strings.TrimSpace(strings.ToLower(b.Gender))
	switch g {
	case "", models.GenderUnspecified:
		g = models.GenderUnspecified
	case models.GenderFemale, models.GenderMale, models.GenderOther:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sexe invalide"})
		return
	}

	hash, err := auth.HashPassword(b.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
		return
	}

	u, err := h.db.CreateUser(r.Context(), store.CreateUserInput{
		Email:        b.Email,
		PasswordHash: hash,
		FirstName:    b.FirstName,
		LastName:     b.LastName,
		BirthDate:    b.BirthDate,
		Gender:       g,
	})
	if errors.Is(err, store.ErrDuplicateEmail) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "email déjà utilisé"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "inscription impossible"})
		return
	}

	if h.cfg.AdminEmail != "" && strings.EqualFold(u.Email, h.cfg.AdminEmail) {
		if err := h.db.UpdateUserRolePlan(r.Context(), u.ID, stringPtr(models.RoleAdmin), nil); err == nil {
			u.Role = models.RoleAdmin
		}
	}

	token, err := auth.SignJWT(u.ID.Hex(), h.cfg.JWTSecret, 7*24*time.Hour)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token"})
		return
	}

	caps, _ := h.capabilitiesForUser(r.Context(), u)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token,
		"user":  userPublic(u, caps),
	})
}

var registerEmailRx = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func registerEmailLooksValid(email string) bool {
	if len(email) < 4 || len(email) > 254 {
		return false
	}
	return registerEmailRx.MatchString(email)
}

// RegisterCheckEmail — public : vérifie format + disponibilité avant l’inscription multi-étapes.
func (h *Handlers) RegisterCheckEmail(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requête invalide"})
		return
	}
	email := strings.TrimSpace(strings.ToLower(b.Email))
	if !registerEmailLooksValid(email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "adresse email invalide"})
		return
	}
	_, err := h.db.FindUserByEmail(r.Context(), email)
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"available": false})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]bool{"available": true})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var b regBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	b.Email = strings.TrimSpace(strings.ToLower(b.Email))

	u, err := h.db.FindUserByEmail(r.Context(), b.Email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identifiants invalides"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
		return
	}
	if !auth.CheckPassword(u.PasswordHash, b.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "identifiants invalides"})
		return
	}
	_ = h.db.SetUserLastSeenNow(r.Context(), u.ID)

	token, err := auth.SignJWT(u.ID.Hex(), h.cfg.JWTSecret, 7*24*time.Hour)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token"})
		return
	}

	caps, capErr := h.capabilitiesForUser(r.Context(), u)
	if capErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  userPublic(u, caps),
	})
}

func userPublic(u *models.User, capabilities map[string]bool) map[string]any {
	m := map[string]any{
		"id":            u.ID.Hex(),
		"email":         u.Email,
		"first_name":    u.FirstName,
		"last_name":     u.LastName,
		"birth_date":    u.BirthDate,
		"gender":        u.Gender,
		"strava_linked": u.HasStrava(),
		"created_at":    u.CreatedAt.Format(time.RFC3339),
		"role":          u.EffectiveRole(),
		"plan":          u.EffectivePlan(),
	}
	if capabilities != nil {
		m["capabilities"] = capabilities
	}
	return m
}

func stringPtr(s string) *string { return &s }

func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	caps, err := h.capabilitiesForUser(r.Context(), u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	writeJSON(w, http.StatusOK, userPublic(u, caps))
}

type patchMeBody struct {
	FirstName       string `json:"first_name"`
	LastName        string `json:"last_name"`
	BirthDate       string `json:"birth_date"`
	Gender          string `json:"gender"`
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func validateProfileFields(firstName, lastName, birthDate string, gender string) (string, string, string, string, string) {
	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	birthDate = strings.TrimSpace(birthDate)
	gender = strings.TrimSpace(strings.ToLower(gender))
	if firstName == "" || lastName == "" {
		return "", "", "", "", "prénom et nom requis"
	}
	if utf8.RuneCountInString(firstName) > 80 || utf8.RuneCountInString(lastName) > 80 {
		return "", "", "", "", "prénom ou nom trop long"
	}
	if birthDate == "" {
		return "", "", "", "", "date de naissance requise"
	}
	bd, err := time.Parse("2006-01-02", birthDate)
	if err != nil {
		return "", "", "", "", "date de naissance invalide (AAAA-MM-JJ)"
	}
	bd = time.Date(bd.Year(), bd.Month(), bd.Day(), 0, 0, 0, 0, time.UTC)
	today := time.Now().UTC()
	todayDay := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if bd.After(todayDay) {
		return "", "", "", "", "date de naissance dans le futur"
	}
	if bd.Before(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return "", "", "", "", "date de naissance invalide"
	}
	birthDate = bd.Format("2006-01-02")
	switch gender {
	case "", models.GenderUnspecified:
		gender = models.GenderUnspecified
	case models.GenderFemale, models.GenderMale, models.GenderOther:
	default:
		return "", "", "", "", "sexe invalide"
	}
	return firstName, lastName, birthDate, gender, ""
}

// PatchMe met à jour le profil ; mot de passe optionnel (current + new).
func (h *Handlers) PatchMe(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b patchMeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	fn, ln, bd, g, vErr := validateProfileFields(b.FirstName, b.LastName, b.BirthDate, b.Gender)
	if vErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": vErr})
		return
	}
	if err := h.db.UpdateUserProfile(r.Context(), u.ID, fn, ln, bd, g); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour impossible"})
		return
	}
	newPwd := strings.TrimSpace(b.NewPassword)
	if newPwd != "" {
		if len(newPwd) < 8 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nouveau mot de passe : 8 caractères minimum"})
			return
		}
		if !auth.CheckPassword(u.PasswordHash, b.CurrentPassword) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "mot de passe actuel incorrect"})
			return
		}
		hash, err := auth.HashPassword(newPwd)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "serveur"})
			return
		}
		if err := h.db.UpdateUserPasswordHash(r.Context(), u.ID, hash); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour impossible"})
			return
		}
	}
	refreshed, err := h.db.FindUserByID(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "serveur"})
		return
	}
	caps, err := h.capabilitiesForUser(r.Context(), refreshed)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	writeJSON(w, http.StatusOK, userPublic(refreshed, caps))
}

type deleteAccountBody struct {
	Password string `json:"password"`
}

// DeleteMyAccount supprime définitivement le compte et toutes les données (conversations, objectifs, courses live).
func (h *Handlers) DeleteMyAccount(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b deleteAccountBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	if !auth.CheckPassword(u.PasswordHash, b.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "mot de passe incorrect"})
		return
	}
	if err := h.db.DeleteUserCascade(r.Context(), u.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) StravaDashboard(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	ok, err := h.userHasCapability(r.Context(), u, "strava_dashboard")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	if !ok {
		writeFeatureForbidden(w, "Strava")
		return
	}
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
		return
	}
	access, err := h.ensureStravaAccess(r.Context(), u)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
		return
	}

	period := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("period")))
	if period == "" {
		period = "30d"
	}
	var after *int64
	now := time.Now().UTC()
	switch period {
	case "7d":
		t := now.AddDate(0, 0, -7).Unix()
		after = &t
	case "30d":
		t := now.AddDate(0, 0, -30).Unix()
		after = &t
	case "90d", "3m":
		t := now.AddDate(0, 0, -90).Unix()
		after = &t
	case "365d", "1y":
		t := now.AddDate(0, 0, -365).Unix()
		after = &t
	case "all":
		after = nil
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "période invalide : 7d, 30d, 90d, 365d ou all",
		})
		return
	}

	runs, err := h.strava.FetchRunActivities(r.Context(), access, after)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
		return
	}
	payload := strava.BuildDashboard(runs, period)
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handlers) StravaAuthorizeURL(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.StravaConfigured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Strava non configuré côté serveur : renseigne STRAVA_CLIENT_ID, STRAVA_CLIENT_SECRET et STRAVA_REDIRECT_URI dans backend/.env",
		})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	ok, err := h.userHasCapability(r.Context(), u, "strava_dashboard")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	if !ok {
		writeFeatureForbidden(w, "Strava")
		return
	}
	state, err := auth.SignStravaState(u.ID.Hex(), h.cfg.JWTSecret, 15*time.Minute)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state"})
		return
	}
	scope := "activity:read_all,profile:read_all"
	url := h.strava.AuthorizeURL(h.cfg.StravaRedirectURI, state, scope)
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func (h *Handlers) StravaCallback(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.StravaConfigured() {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=config", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=missing", http.StatusFound)
		return
	}

	userHex, err := auth.ParseStravaState(state, h.cfg.JWTSecret)
	if err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=state", http.StatusFound)
		return
	}

	oid, err := primitive.ObjectIDFromHex(userHex)
	if err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=user", http.StatusFound)
		return
	}

	u, err := h.db.FindUserByID(r.Context(), oid)
	if err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=user", http.StatusFound)
		return
	}
	ok, err := h.userHasCapability(r.Context(), u, "strava_dashboard")
	if err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=config", http.StatusFound)
		return
	}
	if !ok {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=forbidden", http.StatusFound)
		return
	}

	tokens, err := h.strava.ExchangeCode(r.Context(), code, h.cfg.StravaRedirectURI)
	if err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=strava", http.StatusFound)
		return
	}

	if err := h.db.UpdateStravaTokens(r.Context(), oid, tokens); err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=db", http.StatusFound)
		return
	}

	http.Redirect(w, r, h.cfg.FrontendURL+"/dashboard?strava=ok", http.StatusFound)
}

func (h *Handlers) ensureStravaAccess(ctx context.Context, u *models.User) (string, error) {
	if u.Strava == nil || u.Strava.RefreshToken == "" {
		return "", errors.New("strava not linked")
	}
	tok := u.Strava.AccessToken
	if time.Now().UTC().After(u.Strava.ExpiresAt.Add(-2 * time.Minute)) {
		refreshed, err := h.strava.Refresh(ctx, u.Strava.RefreshToken)
		if err != nil {
			return "", err
		}
		if err := h.db.UpdateStravaTokens(ctx, u.ID, refreshed); err != nil {
			return "", err
		}
		tok = refreshed.AccessToken
		u.Strava = &refreshed
	}
	return tok, nil
}

const chatHistoryMaxTurns = 24 // tours stockés envoyés au modèle (user+assistant)

type chatBody struct {
	Message        string `json:"message"`
	ConversationID string `json:"conversation_id,omitempty"`
}

type goalBody struct {
	DistanceKm      float64 `json:"distance_km"`
	Weeks           int     `json:"weeks"`
	SessionsPerWeek int     `json:"sessions_per_week"`
	TargetTime      string  `json:"target_time"`
}

// validateGoalPayload lit et valide le corps JSON d'un objectif. errHTTP==0 si OK.
func validateGoalPayload(r *http.Request) (b goalBody, label string, distKm float64, targetTime string, errHTTP int, errMsg string) {
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		return goalBody{}, "", 0, "", http.StatusBadRequest, "invalid json"
	}
	label, distKm, ok := goalDistanceLabel(b.DistanceKm)
	if !ok {
		return goalBody{}, "", 0, "", http.StatusBadRequest, "distance_km doit être 5, 10, 21 ou 42"
	}
	if b.Weeks < 1 || b.Weeks > 52 {
		return goalBody{}, "", 0, "", http.StatusBadRequest, "weeks entre 1 et 52"
	}
	if b.SessionsPerWeek < 1 || b.SessionsPerWeek > 7 {
		return goalBody{}, "", 0, "", http.StatusBadRequest, "sessions_per_week entre 1 et 7"
	}
	targetTime = strings.TrimSpace(b.TargetTime)
	if len(targetTime) < 2 {
		return goalBody{}, "", 0, "", http.StatusBadRequest, "indique le temps visé sur la distance (ex. 50 min, 1h45, finir sans chrono précis)"
	}
	if utf8.RuneCountInString(targetTime) > 120 {
		return goalBody{}, "", 0, "", http.StatusBadRequest, "temps visé trop long (120 caractères max)"
	}
	return b, label, distKm, targetTime, 0, ""
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

func goalDistanceLabel(km float64) (string, float64, bool) {
	k := int(math.Round(km))
	normalized := float64(k)
	switch k {
	case 5:
		return "5 km", normalized, true
	case 10:
		return "10 km", normalized, true
	case 21:
		return "21 km (semi-marathon)", normalized, true
	case 42:
		return "42 km (marathon)", normalized, true
	default:
		return "", 0, false
	}
}

func (h *Handlers) CreateConversation(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "coach_chat") {
		return
	}
	conv, err := h.db.CreateConversation(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "impossible de créer la conversation"})
		return
	}
	writeJSON(w, http.StatusCreated, conv)
}

func (h *Handlers) ListConversations(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "coach_chat") {
		return
	}
	list, err := h.db.ListConversationsByUser(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

func (h *Handlers) GetConversation(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "coach_chat") {
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	conv, err := h.db.GetConversationByUser(r.Context(), u.ID, oid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur"})
		return
	}
	writeJSON(w, http.StatusOK, conv)
}

func (h *Handlers) DeleteConversation(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "coach_chat") {
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	err = h.db.DeleteConversationByUser(r.Context(), u.ID, oid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) Chat(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "coach_chat") {
		return
	}

	var b chatBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	b.Message = strings.TrimSpace(b.Message)
	if b.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message vide"})
		return
	}

	var conv *models.Conversation
	var convID primitive.ObjectID
	if strings.TrimSpace(b.ConversationID) == "" {
		c, err := h.db.CreateConversation(r.Context(), u.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "conversation"})
			return
		}
		conv = c
		convID = c.ID
	} else {
		oid, err := primitive.ObjectIDFromHex(strings.TrimSpace(b.ConversationID))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "conversation_id invalide"})
			return
		}
		c, err := h.db.GetConversationByUser(r.Context(), u.ID, oid)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "conversation introuvable"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur"})
			return
		}
		conv = c
		convID = oid
	}

	var system string
	if u.HasStrava() {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
			return
		}
		acts, err := h.strava.ActivitiesSummary(r.Context(), access, 25)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
			return
		}
		actsJSON, _ := json.Marshal(acts)
		system = `Tu es un coach course à pied / vélo (Strava). Tu lis le JSON des activités et réponds en français, ton direct et sympa. ` +
			`Unités : km, minutes, allure en min/km, FC en bpm, dénivelé en m. Jamais de gros pavés : ` +
			`2 à 5 puces courtes OU au plus 3 petits paragraphes de une phrase chacun. ` +
			`Va droit au fait : chiffres clés, lecture en une ligne, puis 1 seul conseil si utile. Pas de listes numérotées longues ni de répétitions. ` +
			`Si une info manque, une seule phrase. ` +
			`Activités récentes (JSON): ` + string(actsJSON)
	} else {
		system = `Tu es un coach course à pied et vélo. Tu réponds en français, ton direct et sympa. ` +
			`Tu n’as pas accès à l’historique Strava de la personne : appuie-toi sur les bonnes pratiques, la progression prudente et des questions courtes si un détail manque. ` +
			`Unités : km, minutes, allure en min/km, FC en bpm, dénivelé en m. Jamais de gros pavés : ` +
			`2 à 5 puces courtes OU au plus 3 petits paragraphes d’une phrase. ` +
			`Va droit au fait. Si une info manque, une seule phrase. ` +
			`Tu peux inviter à associer Strava dans l’app pour des repères basés sur les vraies sorties, sans insister.`
	}

	msgs := []oai.ChatMessage{{Role: "system", Content: system}}
	hist := conv.Messages
	if len(hist) > chatHistoryMaxTurns {
		hist = hist[len(hist)-chatHistoryMaxTurns:]
	}
	for _, t := range hist {
		role := t.Role
		if role != "user" && role != "assistant" {
			continue
		}
		msgs = append(msgs, oai.ChatMessage{Role: role, Content: t.Text})
	}
	msgs = append(msgs, oai.ChatMessage{Role: "user", Content: b.Message})

	reply, err := h.openai.ChatMessages(r.Context(), msgs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}

	var titlePtr *string
	if len(conv.Messages) == 0 {
		t := truncateRunes(b.Message, 52)
		titlePtr = &t
	}
	if err := h.db.AppendConversationTurns(r.Context(), u.ID, convID, b.Message, reply, titlePtr); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sauvegarde message"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"reply":           reply,
		"conversation_id": convID.Hex(),
	})
}

func (h *Handlers) ListGoals(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	list, err := h.db.ListGoalsByUser(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste objectifs"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goals": list})
}

func (h *Handlers) GetGoal(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	g, err := h.db.GetGoalByUser(r.Context(), u.ID, oid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur"})
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (h *Handlers) DeleteGoal(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.DeleteGoalByUser(r.Context(), u.ID, oid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) GoalFeasibility(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}

	b, label, _, targetTime, errHTTP, errMsg := validateGoalPayload(r)
	if errHTTP != 0 {
		writeJSON(w, errHTTP, map[string]string{"error": errMsg})
		return
	}

	userQ := `Objectif course : ` + label + `.
Chrono ou intention : ` + targetTime + `.
Échéance dans ` + strconv.Itoa(b.Weeks) + ` semaine(s).
Disponibilité : ` + strconv.Itoa(b.SessionsPerWeek) + ` séance(s) par semaine en moyenne.
Donne uniquement le verdict et la justification demandés.`

	var system string
	if u.HasStrava() {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
			return
		}
		acts, err := h.strava.ActivitiesSummary(r.Context(), access, 50)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
			return
		}
		actsJSON, _ := json.Marshal(acts)
		system = `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT, phrases courtes (une idée par phrase). Niveau débutant : évite le jargon ou explique en une parenthèse (ex. « allure = minutes pour faire 1 km »).

Tu reçois des activités Strava en JSON + un objectif (distance, chrono visé, semaines avant la course, séances/semaine).

Ton ton est bienveillant et **inclusif** : pas de stéréotypes de genre ou de « niveau normal » ; formulations ouvertes. Rappelle en une phrase qu'on peut parler du ressenti, de la fatigue ou des douleurs avec le coach dans le fil prévu sur l'objectif.

Réponds UNIQUEMENT avec ce format Markdown (rien d'autre — pas de plan d'entraînement ) :

## Verdict
Une seule ligne parmi :
**Réaliste** ou **Ambitieux mais jouable** ou **Très tendu / peu réaliste**

## En une phrase
Une phrase simple qui résume pourquoi.

## Pourquoi (tes données)
3 à 5 puces maximum. Appuie-toi sur le JSON (allures, fréquence des sorties, distances). Pas de chiffres inventés.

## Conseil si tu veux progresser
2 ou 3 puces : que pourrais-tu ajuster (chrono plus large, plus de semaines, ou plus de séances) ?

Ne invente pas de chiffres absents du JSON.

**Activités (JSON) :** ` + string(actsJSON)
	} else {
		system = `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT, phrases courtes. Niveau débutant : évite le jargon ou explique en une parenthèse (ex. « allure = minutes pour faire 1 km »).

Tu n’as **pas** d’historique d’activités : tu te bases **uniquement** sur l’objectif déclaré (distance, chrono ou intention, délai, séances par semaine). Reste prudent : repères généraux, pas de stats inventées. Une phrase peut inviter à lier Strava plus tard pour affiner avec de vraies sorties.

Ton ton est bienveillant et **inclusif**.

Réponds UNIQUEMENT avec ce format Markdown (rien d'autre — pas de plan d'entraînement ) :

## Verdict
Une seule ligne parmi :
**Réaliste** ou **Ambitieux mais jouable** ou **Très tendu / peu réaliste**

## En une phrase
Une phrase simple qui résume pourquoi.

## Pourquoi (sans historique importé)
3 à 5 puces : raisonne sur le délai, le volume hebdomadaire plausible et l’ambition du chrono par rapport à une progression typique — sans prétendre connaître l’entraînement réel de la personne.

## Conseil si tu veux progresser
2 ou 3 puces : ajuster le chrono, le délai ou le nombre de séances ; mentionner que lier Strava permet un avis plus précis.`
	}

	text, err := h.openai.Chat(r.Context(), system, userQ)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"feasibility": text})
}

func (h *Handlers) CreateGoal(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}

	b, label, distKm, targetTime, errHTTP, errMsg := validateGoalPayload(r)
	if errHTTP != 0 {
		writeJSON(w, errHTTP, map[string]string{"error": errMsg})
		return
	}

	hasStravaData := u.HasStrava()
	var actsJSON []byte
	if hasStravaData {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
			return
		}
		acts, err := h.strava.ActivitiesSummary(r.Context(), access, 50)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
			return
		}
		actsJSON, _ = json.Marshal(acts)
	} else {
		actsJSON = []byte("[]")
	}

	plan, planned, err := h.synthesizeTrainingPlan(r.Context(), actsJSON, label, targetTime, b.Weeks, b.SessionsPerWeek, hasStravaData)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}

	now := time.Now().UTC()
	welcome := models.ChatTurn{
		Role: "assistant",
		Text: "Salut — je suis là pour t’accompagner sur cet objectif, à ton rythme.\n\n" +
			"Comment tu te sens en ce moment (énergie, sommeil, stress) ? As-tu des douleurs ou une zone du corps qui t’inquiète ?\n\n" +
			"Écris-moi après tes sorties si tu veux : on pourra alléger, ajuster le chrono ou le délai ensemble, sans pression.",
		CreatedAt: now,
	}

	g := &models.Goal{
		UserID:                u.ID,
		DistanceKm:            distKm,
		DistanceLabel:         label,
		Weeks:                 b.Weeks,
		SessionsPerWeek:       b.SessionsPerWeek,
		TargetTime:            targetTime,
		Plan:                  plan,
		PlannedSessions:       planned,
		CoachThread:           []models.ChatTurn{welcome},
		PlanWithoutStravaData: !hasStravaData,
	}
	if err := h.db.CreateGoal(r.Context(), g); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sauvegarde objectif"})
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

const goalCoachMaxTurns = 20

type goalChatBody struct {
	Message string `json:"message"`
}

func (h *Handlers) GoalChat(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}

	idHex := chi.URLParam(r, "id")
	gid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}

	g, err := h.db.GetGoalByUser(r.Context(), u.ID, gid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur"})
		return
	}

	var b goalChatBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	b.Message = strings.TrimSpace(b.Message)
	if b.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message vide"})
		return
	}

	hasStravaData := u.HasStrava()
	var actsJSON []byte
	if hasStravaData {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
			return
		}
		acts, err := h.strava.ActivitiesSummary(r.Context(), access, 50)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
			return
		}
		actsJSON, _ = json.Marshal(acts)
	} else {
		actsJSON = []byte("[]")
	}

	aiIntent := h.extractGoalAdjustIntent(r.Context(), b.Message, g)
	mergedIntent := mergeGoalAdjustIntent(aiIntent, heuristicGoalAdjust(b.Message, g))
	spw, weeks, target := mergedGoalParams(g, mergedIntent)
	calOff := calendarOffsetsFor(spw, mergedIntent.AvoidWednesday)
	if spw != g.SessionsPerWeek || weeks != g.Weeks || target != strings.TrimSpace(g.TargetTime) {
		mergedIntent.Replan = true
	}
	if structuralCalendarChange(g, calOff) {
		mergedIntent.Replan = true
	}
	if needsPersistedReplan(g, mergedIntent, spw, weeks, target, calOff) && strings.TrimSpace(h.cfg.OpenAIAPIKey) != "" {
		plan, planned, genErr := h.synthesizeTrainingPlan(r.Context(), actsJSON, g.DistanceLabel, target, weeks, spw, hasStravaData)
		if genErr == nil {
			upErr := h.db.UpdateGoalTrainingFields(r.Context(), u.ID, gid, plan, planned, weeks, spw, target, calOff, !hasStravaData)
			if upErr == nil {
				refreshed, refErr := h.db.GetGoalByUser(r.Context(), u.ID, gid)
				if refErr == nil {
					g = refreshed
				}
			}
		}
	}

	planCtx := g.Plan
	const planMax = 3200
	if len(planCtx) > planMax {
		planCtx = planCtx[:planMax] + "\n… (suite du plan omise pour le contexte)"
	}

	activitiesBlock := "**Activités récentes (JSON)**\n" + string(actsJSON)
	if !hasStravaData {
		activitiesBlock = "**Historique Strava** : non importé — base tes réponses sur l’objectif et le plan ci-dessus. Tu peux mentionner qu’associer Strava permet d’aligner les conseils sur le volume et l’allure réels, sans insister."
	}

	system := `Tu es un·e coach course à pied bienveillant·e. Tu écris en français.

**Style et inclusion**
- TUTOIEMENT par défaut ; si la personne se vouvoie (« je vous », etc.), passe au vouvoiement sans en faire tout un plat.
- Inclusi·f·ve : pas de stéréotypes de genre, de corps ou de « niveau habituel » ; reste neutre et respectueu·x·se.
- Accueille toutes les réalités (retour à la course, santé variable, manque de temps).

**Rôle**
Tu discutes de L'OBJECTIF enregistré (distance, chrono, semaines, séances/semaine) et de son plan. Les valeurs affichées dans **Objectif enregistré** sont à jour : si la personne vient de demander moins de séances ou un autre calendrier, le serveur a peut‑être déjà régénéré et enregistré un **nouveau plan** — confirme clairement ce qui a changé (ex. nombre de séances, répartition) en **2–4 phrases**, sans recopier tout le Markdown du plan.

**Ressenti et santé**
- Demande ou rebondis sur : fatigue, sommeil, stress, humeur, douleurs ou gênes.
- Tu ne diagnostiques pas. Si douleur forte, persistante ou inquiétante : encourage à consulter un·e professionnel·le de santé.

**Forme des réponses**
3 à 8 phrases en général, ou quelques puces courtes. Si tu détailles une séance ou un ajustement, donne des **allures min/km** et des **temps par répétition** quand c’est du fractionné.

**Objectif enregistré**
- Distance : ` + g.DistanceLabel + `
- Chrono visé : ` + g.TargetTime + `
- Délai : ` + strconv.Itoa(g.Weeks) + ` semaine(s)
- Séances / semaine : ` + strconv.Itoa(g.SessionsPerWeek) + `

**Plan (référence)**
` + planCtx + `

` + activitiesBlock

	msgs := []oai.ChatMessage{{Role: "system", Content: system}}
	hist := g.CoachThread
	if len(hist) > goalCoachMaxTurns {
		hist = hist[len(hist)-goalCoachMaxTurns:]
	}
	for _, t := range hist {
		if t.Role != "user" && t.Role != "assistant" {
			continue
		}
		msgs = append(msgs, oai.ChatMessage{Role: t.Role, Content: t.Text})
	}
	msgs = append(msgs, oai.ChatMessage{Role: "user", Content: b.Message})

	reply, err := h.openai.ChatMessages(r.Context(), msgs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}

	if err := h.db.AppendGoalCoachTurns(r.Context(), u.ID, gid, b.Message, reply); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sauvegarde"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
}

type ctxUser struct{}

func UserFromID(ctx context.Context, db *store.DB, idHex string) (*models.User, error) {
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return nil, err
	}
	return db.FindUserByID(ctx, oid)
}

func (h *Handlers) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(strings.ToLower(hdr), "bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "non authentifié"})
			return
		}
		token := strings.TrimSpace(hdr[7:])
		claims, err := auth.ParseJWT(token, h.cfg.JWTSecret)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "token invalide"})
			return
		}
		u, err := UserFromID(r.Context(), h.db, claims.UserID)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "utilisateur introuvable"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
			return
		}
		_ = h.db.TouchUserLastSeenIfStale(r.Context(), u.ID, 3*time.Minute)
		ctx := context.WithValue(r.Context(), ctxUser{}, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handlers) Mount(r chi.Router) {
	r.Post("/auth/register/check-email", h.RegisterCheckEmail)
	r.Post("/auth/register", h.Register)
	r.Post("/auth/login", h.Login)
	r.Get("/strava/callback", h.StravaCallback)
	r.Get("/public/offer-config", h.PublicOfferConfig)

	r.Route("/admin", func(ar chi.Router) {
		ar.Use(h.AuthMiddleware)
		ar.Use(h.AdminMiddleware)
		ar.Get("/stats", h.AdminStats)
		ar.Get("/users", h.AdminListUsers)
		ar.Get("/users/{id}", h.AdminGetUser)
		ar.Patch("/users/{id}", h.AdminPatchUser)
		ar.Delete("/users/{id}", h.AdminDeleteUser)
		ar.Get("/promo-codes", h.AdminListPromos)
		ar.Post("/promo-codes", h.AdminCreatePromo)
		ar.Patch("/promo-codes/{id}", h.AdminPatchPromo)
		ar.Delete("/promo-codes/{id}", h.AdminDeletePromo)
		ar.Get("/offer-config", h.AdminGetOfferConfig)
		ar.Put("/offer-config", h.AdminPutOfferConfig)
		ar.Get("/circuits", h.AdminListCircuits)
		ar.Get("/circuit-times/search", h.AdminSearchCircuitTimesByUser)
		ar.Get("/circuits/{id}/times", h.AdminListCircuitTimes)
		ar.Patch("/circuits/{id}", h.AdminPatchCircuit)
		ar.Delete("/circuits/{id}", h.AdminDeleteCircuit)
		ar.Delete("/circuit-times/{id}", h.AdminDeleteCircuitTime)
	})

	r.Group(func(pr chi.Router) {
		pr.Use(h.AuthMiddleware)
		pr.Post("/checkout/preview", h.CheckoutPreview)
		pr.Post("/checkout/subscribe", h.CheckoutSubscribe)
		pr.Get("/me", h.Me)
		pr.Patch("/me", h.PatchMe)
		pr.Post("/me/delete-account", h.DeleteMyAccount)
		pr.Get("/strava/dashboard", h.StravaDashboard)
		pr.Get("/strava/forecast", h.StravaRaceForecast)
		pr.Post("/strava/forecast/adjust", h.StravaRaceForecastAdjust)
		pr.Get("/strava/authorize", h.StravaAuthorizeURL)
		pr.Post("/conversations", h.CreateConversation)
		pr.Get("/conversations", h.ListConversations)
		pr.Get("/conversations/{id}", h.GetConversation)
		pr.Delete("/conversations/{id}", h.DeleteConversation)
		pr.Post("/goals/feasibility", h.GoalFeasibility)
		pr.Post("/goals", h.CreateGoal)
		pr.Get("/goals", h.ListGoals)
		pr.Post("/goals/{id}/chat", h.GoalChat)
		pr.Delete("/goals/{id}", h.DeleteGoal)
		pr.Get("/goals/{id}/calendar", h.GoalCalendar)
		pr.Get("/goals/{id}", h.GetGoal)
		pr.Post("/chat", h.Chat)
		pr.Post("/live-runs", h.CreateLiveRun)
		pr.Get("/live-runs", h.ListLiveRuns)
		pr.Get("/live-runs/{id}", h.GetLiveRun)
		pr.Get("/circuits/near", h.CircuitsNear)
		pr.Post("/circuits", h.CreateCircuit)
		pr.Get("/circuits/{id}", h.GetCircuit)
		pr.Post("/circuits/{id}/times", h.PostCircuitTime)
	})
}
