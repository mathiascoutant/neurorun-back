package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
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
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	var b regBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	b.Email = strings.TrimSpace(strings.ToLower(b.Email))
	if b.Email == "" || len(b.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email et mot de passe (8+ caractères) requis"})
		return
	}

	hash, err := auth.HashPassword(b.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur serveur"})
		return
	}

	u, err := h.db.CreateUser(r.Context(), b.Email, hash)
	if errors.Is(err, store.ErrDuplicateEmail) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "email déjà utilisé"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "inscription impossible"})
		return
	}

	token, err := auth.SignJWT(u.ID.Hex(), h.cfg.JWTSecret, 7*24*time.Hour)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token,
		"user":  userPublic(u),
	})
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

	token, err := auth.SignJWT(u.ID.Hex(), h.cfg.JWTSecret, 7*24*time.Hour)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token": token,
		"user":  userPublic(u),
	})
}

func userPublic(u *models.User) map[string]any {
	return map[string]any{
		"id":            u.ID.Hex(),
		"email":         u.Email,
		"strava_linked": u.HasStrava(),
		"created_at":    u.CreatedAt.Format(time.RFC3339),
	}
}

func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	writeJSON(w, http.StatusOK, userPublic(u))
}

func (h *Handlers) StravaAuthorizeURL(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.StravaConfigured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Strava non configuré côté serveur : renseigne STRAVA_CLIENT_ID, STRAVA_CLIENT_SECRET et STRAVA_REDIRECT_URI dans backend/.env",
		})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
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

	tokens, err := h.strava.ExchangeCode(r.Context(), code, h.cfg.StravaRedirectURI)
	if err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=strava", http.StatusFound)
		return
	}

	if err := h.db.UpdateStravaTokens(r.Context(), oid, tokens); err != nil {
		http.Redirect(w, r, h.cfg.FrontendURL+"/link-strava?error=db", http.StatusFound)
		return
	}

	http.Redirect(w, r, h.cfg.FrontendURL+"/chat?strava=ok", http.StatusFound)
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
	conv, err := h.db.CreateConversation(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "impossible de créer la conversation"})
		return
	}
	writeJSON(w, http.StatusCreated, conv)
}

func (h *Handlers) ListConversations(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	list, err := h.db.ListConversationsByUser(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

func (h *Handlers) GetConversation(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
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

func (h *Handlers) Chat(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
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

	system := `Tu es un coach course à pied / vélo (Strava). Tu lis le JSON des activités et réponds en français, ton direct et sympa. ` +
		`Unités : km, minutes, allure en min/km, FC en bpm, dénivelé en m. Jamais de gros pavés : ` +
		`2 à 5 puces courtes OU au plus 3 petits paragraphes de une phrase chacun. ` +
		`Va droit au fait : chiffres clés, lecture en une ligne, puis 1 seul conseil si utile. Pas de listes numérotées longues ni de répétitions. ` +
		`Si une info manque, une seule phrase. ` +
		`Activités récentes (JSON): ` + string(actsJSON)

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
	list, err := h.db.ListGoalsByUser(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste objectifs"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goals": list})
}

func (h *Handlers) GetGoal(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
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

func (h *Handlers) CreateGoal(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
		return
	}

	var b goalBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	label, distKm, ok := goalDistanceLabel(b.DistanceKm)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "distance_km doit être 5, 10, 21 ou 42"})
		return
	}
	if b.Weeks < 1 || b.Weeks > 52 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "weeks entre 1 et 52"})
		return
	}
	if b.SessionsPerWeek < 1 || b.SessionsPerWeek > 7 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessions_per_week entre 1 et 7"})
		return
	}
	targetTime := strings.TrimSpace(b.TargetTime)
	if len(targetTime) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "indique le temps visé sur la distance (ex. 50 min, 1h45, finir sans chrono précis)"})
		return
	}
	if utf8.RuneCountInString(targetTime) > 120 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "temps visé trop long (120 caractères max)"})
		return
	}

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

	system := `Tu es un coach course à pied expert (français). Tu reçois jusqu'à 50 activités Strava récentes en JSON + un objectif (distance, chrono visé, nombre de semaines avant la course, séances/semaine).

Rédige un plan d'entraînement détaillé en Markdown. Ordre imposé :

1. **Faisabilité du chrono visé** (obligatoire, en premier, 4 à 8 phrases max) : à partir UNIQUEMENT des tendances dans le JSON (allures, distances, régularité, volume) et des paramètres de l'objectif (distance, temps vis-à-vis de ce que montrent les sorties, nombre de semaines restantes, nombre de séances/semaine), dis clairement si l'objectif te paraît **réaliste**, **ambitieux mais jouable**, ou **très tendu / peu réaliste** dans ces conditions. Si c'est trop tendu, dis-le franchement et propose une alternative (chrono intermédiaire, plus de semaines, ou plus de séances si pertinent). Ne invente pas de chiffres absents des activités.

2. **Synthèse forme actuelle** (3 à 6 puces, données JSON seulement).

3. **Principes** (progression, récup, intensité vs volume).

4. **Semaine par semaine** : volume approximatif, sortie longue, séance de qualité si adapté, récup. Respecte le nombre de séances par semaine demandé.

5. **Affûtage** si l'échéance le justifie.

6. **Rappels sécurité** (douleur persistante = stop + avis pro).

**Activités (JSON) :** ` + string(actsJSON)

	userQ := `Objectif course : ` + label + `.
Chrono ou intention chrono visée : ` + targetTime + `.
Échéance dans ` + strconv.Itoa(b.Weeks) + ` semaine(s).
Disponibilité : ` + strconv.Itoa(b.SessionsPerWeek) + ` séance(s) par semaine en moyenne.
Rédige le plan et commence par le bloc « Faisabilité du chrono visé » comme demandé.`

	plan, err := h.openai.Chat(r.Context(), system, userQ)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}

	g := &models.Goal{
		UserID:          u.ID,
		DistanceKm:      distKm,
		DistanceLabel:   label,
		Weeks:           b.Weeks,
		SessionsPerWeek: b.SessionsPerWeek,
		TargetTime:      targetTime,
		Plan:            plan,
	}
	if err := h.db.CreateGoal(r.Context(), g); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sauvegarde objectif"})
		return
	}
	writeJSON(w, http.StatusCreated, g)
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
	r.Post("/auth/register", h.Register)
	r.Post("/auth/login", h.Login)
	r.Get("/strava/callback", h.StravaCallback)

	r.Group(func(pr chi.Router) {
		pr.Use(h.AuthMiddleware)
		pr.Get("/me", h.Me)
		pr.Get("/strava/authorize", h.StravaAuthorizeURL)
		pr.Post("/conversations", h.CreateConversation)
		pr.Get("/conversations", h.ListConversations)
		pr.Get("/conversations/{id}", h.GetConversation)
		pr.Post("/goals", h.CreateGoal)
		pr.Get("/goals", h.ListGoals)
		pr.Get("/goals/{id}", h.GetGoal)
		pr.Post("/chat", h.Chat)
	})
}
