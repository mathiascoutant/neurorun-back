package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

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
		"id":          u.ID.Hex(),
		"email":       u.Email,
		"strava_linked": u.HasStrava(),
		"created_at":  u.CreatedAt.Format(time.RFC3339),
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

type chatBody struct {
	Message string `json:"message"`
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

	system := `Tu es un coach course à pied et vélo (style Strava). Tu analyses les activités fournies (JSON) et réponds en français de façon concise et encourageante. ` +
		`Chaque activité utilise des unités lisibles : distance en kilomètres (distance_km), durées en minutes (moving_time_min, elapsed_time_min), ` +
		`vitesses en km/h (average_speed_kmh), allure moyenne éventuelle en minutes par km (pace_min_per_km), dénivelé en mètres (elevation_gain_m), fréquence cardiaque en bpm. ` +
		`Dans tes réponses, parle toujours en km et en minutes (pas en mètres ni en secondes pour décrire une sortie). Pour l’allure, utilise min/km ou une formulation équivalente. ` +
		`Si les données manquent pour une question, dis-le. Donne des conseils pratiques (allure, récup, volume). ` +
		`Activités récentes (JSON): ` + string(actsJSON)

	reply, err := h.openai.Chat(r.Context(), system, b.Message)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
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
		pr.Post("/chat", h.Chat)
	})
}
