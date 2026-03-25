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

func (h *Handlers) GoalFeasibility(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
		return
	}

	b, label, _, targetTime, errHTTP, errMsg := validateGoalPayload(r)
	if errHTTP != 0 {
		writeJSON(w, errHTTP, map[string]string{"error": errMsg})
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

	system := `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT, phrases courtes (une idée par phrase). Niveau débutant : évite le jargon ou explique en une parenthèse (ex. « allure = minutes pour faire 1 km »).

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

	userQ := `Objectif course : ` + label + `.
Chrono ou intention : ` + targetTime + `.
Échéance dans ` + strconv.Itoa(b.Weeks) + ` semaine(s).
Disponibilité : ` + strconv.Itoa(b.SessionsPerWeek) + ` séance(s) par semaine en moyenne.
Donne uniquement le verdict et la justification demandés.`

	text, err := h.openai.Chat(r.Context(), system, userQ)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"feasibility": text})
}

func (h *Handlers) CreateGoal(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
		return
	}

	b, label, distKm, targetTime, errHTTP, errMsg := validateGoalPayload(r)
	if errHTTP != 0 {
		writeJSON(w, errHTTP, map[string]string{"error": errMsg})
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

	system := `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT. Style clair : phrases courtes, listes à puces, peu de blocs denses. Niveau accessible : si tu cites min/km, rappelle une fois que c'est « minutes pour parcourir 1 km ».

Tu reçois des activités Strava en JSON + un objectif (distance, chrono, semaines restantes, séances/semaine).

Rédige un plan en Markdown avec EXACTEMENT ces sections (titres ## comme ci-dessous), dans l'ordre :

## Rappel faisabilité
L'utilisateur a déjà lu un avis détaillé. 2 phrases maximum : à quel point le chrono est cohérent avec ses sorties. Pas de répétition longue.

## Où tu en es aujourd'hui
4 à 6 puces max, uniquement à partir du JSON (allures, volume, régularité). Formulations simples.

## Les 3 idées à retenir
Exactement 3 puces courtes : ce qui va t'aider à progresser sans te blesser.

## Calendrier — semaine par semaine
Pour chaque semaine, utilise un sous-titre ### Semaine 1, ### Semaine 2, etc. (autant que les semaines disponibles jusqu'à la course).
Dans chaque semaine : puces courtes (Séance 1, Séance 2…) avec volume indicatif en km et type d'effort (facile / moyen / un peu plus vite). Respecte le nombre de séances par semaine demandé. Si une seule séance/semaine, une seule puce claire.

## Dans les derniers jours avant la course
2 à 4 puces : repos, dernier petit effort éventuel, pas de gros volume.

## Sécurité
2 puces : douleur anormale = arrêt et avis médical ; hydratation et écoute du corps.

## Échanges avec le coach
2 puces courtes : invite à utiliser le fil de discussion sous cet objectif pour dire comment tu te sens (forme, sommeil, stress), parler de gênes ou douleurs, et ajuster ensemble le rythme ou le chrono si besoin — sans jugement.

Pas de paragraphes de plus de 3 phrases d'affilée. Pas de listes numérotées longues.

**Activités (JSON) :** ` + string(actsJSON)

	userQ := `Objectif course : ` + label + `.
Chrono ou intention : ` + targetTime + `.
Échéance dans ` + strconv.Itoa(b.Weeks) + ` semaine(s).
Disponibilité : ` + strconv.Itoa(b.SessionsPerWeek) + ` séance(s) par semaine en moyenne.
Rédige le plan complet en respectant les titres et le style demandés. Pas de vouvoiement.`

	plan, err := h.openai.Chat(r.Context(), system, userQ)
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
		UserID:          u.ID,
		DistanceKm:      distKm,
		DistanceLabel:   label,
		Weeks:           b.Weeks,
		SessionsPerWeek: b.SessionsPerWeek,
		TargetTime:      targetTime,
		Plan:            plan,
		CoachThread:     []models.ChatTurn{welcome},
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
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
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

	planCtx := g.Plan
	const planMax = 3200
	if len(planCtx) > planMax {
		planCtx = planCtx[:planMax] + "\n… (suite du plan omise pour le contexte)"
	}

	system := `Tu es un·e coach course à pied bienveillant·e. Tu écris en français.

**Style et inclusion**
- TUTOIEMENT par défaut ; si la personne se vouvoie (« je vous », etc.), passe au vouvoiement sans en faire tout un plat.
- Inclusi·f·ve : pas de stéréotypes de genre, de corps ou de « niveau habituel » ; reste neutre et respectueu·x·se.
- Accueille toutes les réalités (retour à la course, santé variable, manque de temps).

**Rôle**
Tu discutes de L'OBJECTIF déjà enregistré (distance, chrono visé, semaines, séances/semaine) et de son plan. Tu peux proposer d'**ajuster** charge, délai ou chrono si la personne dit que ça ne va pas — une piste simple, sans culpabiliser.

**Ressenti et santé**
- Demande ou rebondis sur : fatigue, sommeil, stress, humeur, douleurs ou gênes.
- Tu ne diagnostiques pas. Si douleur forte, persistante ou inquiétante : encourage à consulter un·e professionnel·le de santé.

**Forme des réponses**
3 à 8 phrases en général, ou quelques puces courtes. Pas de réécriture complète du plan sauf demande explicite.

**Objectif enregistré**
- Distance : ` + g.DistanceLabel + `
- Chrono visé : ` + g.TargetTime + `
- Délai : ` + strconv.Itoa(g.Weeks) + ` semaine(s)
- Séances / semaine : ` + strconv.Itoa(g.SessionsPerWeek) + `

**Plan (référence)**
` + planCtx + `

**Activités récentes (JSON)**
` + string(actsJSON)

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
		pr.Post("/goals/feasibility", h.GoalFeasibility)
		pr.Post("/goals", h.CreateGoal)
		pr.Get("/goals", h.ListGoals)
		pr.Post("/goals/{id}/chat", h.GoalChat)
		pr.Get("/goals/{id}", h.GetGoal)
		pr.Post("/chat", h.Chat)
	})
}
