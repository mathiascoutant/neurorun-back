package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/scene3d"
	"runapp/internal/store"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/go-chi/chi/v5"
)

const (
	// shopModel3DMaxPhotoBytes : limite de l'API Anthropic pour une image. Le navigateur réduit les
	// photos bien en dessous avant l'envoi.
	shopModel3DMaxPhotoBytes  int64 = 5 << 20
	shopModel3DMaxPhotos            = 30
	shopModel3DMaxInstruction       = 2000
	shopModel3DMaxRenders           = 4
	shopModel3DTimeout              = 10 * time.Minute
)

// sceneGenerator : l'appel au modèle, remplacé dans les tests.
type sceneGenerator interface {
	Generate(ctx context.Context, req scene3d.Request) (*scene3d.Scene, error)
}

type shopModel3DHistory struct {
	Instruction string    `json:"instruction,omitempty"`
	Summary     string    `json:"summary"`
	CreatedAt   time.Time `json:"created_at"`
}

func (h *Handlers) writeShopModel3D(w http.ResponseWriter, status int, m *models.ShopModel3D) {
	photos := m.Photos
	if photos == nil {
		photos = []string{}
	}
	generating := m.Status == models.ShopModel3DGenerating
	failure := m.Error
	if generating && time.Since(m.StartedAt) > shopModel3DTimeout {
		generating = false
		failure = "La génération a été interrompue : relance-la."
	}
	history := make([]shopModel3DHistory, len(m.Versions))
	for i, v := range m.Versions {
		history[i] = shopModel3DHistory{Instruction: v.Instruction, Summary: v.Summary, CreatedAt: v.CreatedAt}
	}
	var scene json.RawMessage
	current := ""
	if len(m.Versions) > 0 {
		current = m.Versions[len(m.Versions)-1].Scene
		scene = json.RawMessage(current)
	}
	writeJSON(w, status, map[string]any{
		"enabled":    h.sceneGenerator != nil,
		"photos":     photos,
		"generating": generating,
		"error":      failure,
		"scene":      scene,
		"history":    history,
		"published":  m.Published != "",
		// published_current : la boutique montre bien la version affichée ici.
		"published_current": m.Published != "" && m.Published == current,
	})
}

// AdminGetShopModel3D GET — photos, scène actuelle et état de la génération (relu pendant l'attente).
func (h *Handlers) AdminGetShopModel3D(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	m, err := h.db.GetShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "modélisation indisponible"})
		return
	}
	h.writeShopModel3D(w, http.StatusOK, m)
}

// AdminUploadShopModel3DPhoto POST multipart — ajoute une photo de travail, hors de la galerie du produit.
func (h *Handlers) AdminUploadShopModel3DPhoto(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, shopModel3DMaxPhotoBytes+(1<<16))
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "photo manquante ou trop lourde (5 Mo maximum)"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || int64(len(data)) > shopModel3DMaxPhotoBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "photo trop lourde (5 Mo maximum)"})
		return
	}
	allowed, ok := shopAllowedMedia[http.DetectContentType(data)]
	if !ok || allowed.Kind != models.ShopMediaImage {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format non pris en charge : JPEG, PNG ou WebP"})
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stockage indisponible"})
		return
	}
	id := hex.EncodeToString(raw) + allowed.Ext
	if err := os.MkdirAll(h.cfg.ShopMediaDir, 0o755); err != nil {
		log.Printf("modélisation: dossier médias: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stockage indisponible"})
		return
	}
	if err := os.WriteFile(filepath.Join(h.cfg.ShopMediaDir, id), data, 0o644); err != nil {
		log.Printf("modélisation: enregistrement photo: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stockage indisponible"})
		return
	}
	if err := h.db.AddShopModel3DPhoto(r.Context(), p.ID, id, shopModel3DMaxPhotos); err != nil {
		h.removeShopMediaFile(id)
		if errors.Is(err, store.ErrShopModel3DFull) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "30 photos maximum par article"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// AdminDeleteShopModel3DPhoto DELETE — retire une photo de travail. Une impression qui la reprenait
// disparaît du rendu : l'interface le signale avant la suppression.
func (h *Handlers) AdminDeleteShopModel3DPhoto(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	photoID := chi.URLParam(r, "photoId")
	m, err := h.db.GetShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	if slices.Contains(m.PublishedPhotos, photoID) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "cette photo sert à la vue 3D de la boutique : republie sans elle ou retire la vue d’abord"})
		return
	}
	if err := h.db.RemoveShopModel3DPhoto(r.Context(), p.ID, photoID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "photo introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	h.removeShopMediaFile(photoID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// AdminGenerateShopModel3D POST — lance une génération ou une retouche et répond tout de suite. Elle
// dure plusieurs minutes, plus que ce que laisse passer un proxy : le navigateur relit l'état.
func (h *Handlers) AdminGenerateShopModel3D(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	if h.sceneGenerator == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ANTHROPIC_API_KEY n’est pas configurée sur le serveur"})
		return
	}
	var b struct {
		Instruction string `json:"instruction"`
		// Restart : repartir des photos seules, en ignorant la scène actuelle.
		Restart bool `json:"restart"`
		// Renders : captures JPEG de la scène actuelle, en base64.
		Renders []string `json:"renders"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "demande invalide ou trop lourde"})
		return
	}
	renders, ok := decodeShopModel3DRenders(b.Renders)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "captures du rendu invalides"})
		return
	}
	b.Instruction = strings.TrimSpace(b.Instruction)
	if len([]rune(b.Instruction)) > shopModel3DMaxInstruction {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "consigne trop longue (2000 caractères maximum)"})
		return
	}
	m, err := h.db.GetShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "modélisation indisponible"})
		return
	}
	if len(m.Photos) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ajoute au moins une photo du produit"})
		return
	}
	if len(m.Versions) > 0 && !b.Restart && b.Instruction == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "écris ce qu’il faut changer"})
		return
	}
	photos, err := h.readShopModel3DPhotos(m.Photos)
	if err != nil {
		log.Printf("modélisation: lecture photos: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "photos illisibles : supprime-les et renvoie-les"})
		return
	}
	now := time.Now()
	started, err := h.db.BeginShopModel3D(r.Context(), p.ID, now, now.Add(-shopModel3DTimeout))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "modélisation indisponible"})
		return
	}
	if !started {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "une génération est déjà en cours"})
		return
	}

	req := scene3d.Request{ProductName: p.Name, Description: p.Description, Photos: photos, Instruction: b.Instruction}
	if len(m.Versions) > 0 && !b.Restart {
		req.Current = m.Versions[len(m.Versions)-1].Scene
		req.Renders = renders
	}
	go h.runShopModel3D(p.ID.Hex(), m, req)

	m.Status = models.ShopModel3DGenerating
	m.StartedAt = now
	m.Error = ""
	h.writeShopModel3D(w, http.StatusAccepted, m)
}

// decodeShopModel3DRenders vérifie les captures envoyées par le navigateur : quatre JPEG au plus.
func decodeShopModel3DRenders(encoded []string) ([]scene3d.Photo, bool) {
	if len(encoded) > shopModel3DMaxRenders {
		return nil, false
	}
	renders := make([]scene3d.Photo, 0, len(encoded))
	for _, e := range encoded {
		data, err := base64.StdEncoding.DecodeString(e)
		if err != nil || http.DetectContentType(data) != "image/jpeg" {
			return nil, false
		}
		renders = append(renders, scene3d.Photo{ContentType: "image/jpeg", Data: data})
	}
	return renders, true
}

func (h *Handlers) readShopModel3DPhotos(ids []string) ([]scene3d.Photo, error) {
	photos := make([]scene3d.Photo, 0, len(ids))
	for _, id := range ids {
		if !shopMediaName.MatchString(id) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(h.cfg.ShopMediaDir, id))
		if err != nil {
			return nil, err
		}
		photos = append(photos, scene3d.Photo{ID: id, ContentType: http.DetectContentType(data), Data: data})
	}
	return photos, nil
}

// runShopModel3D tourne après la réponse HTTP : son contexte ne dépend pas de la requête.
func (h *Handlers) runShopModel3D(label string, m *models.ShopModel3D, req scene3d.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), shopModel3DTimeout)
	defer cancel()

	var version *models.ShopModel3DVersion
	failure := ""
	scene, err := h.sceneGenerator.Generate(ctx, req)
	if err == nil {
		var raw []byte
		raw, err = json.Marshal(scene)
		if err == nil {
			version = &models.ShopModel3DVersion{
				Instruction: req.Instruction,
				Summary:     scene.Summary,
				Scene:       string(raw),
				CreatedAt:   time.Now(),
			}
		}
	}
	if err != nil {
		log.Printf("modélisation %s: %v", label, err)
		failure = shopModel3DErrorMessage(err)
	}
	// Contexte neuf : même après l'expiration de `ctx`, la réservation doit être libérée.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer saveCancel()
	if err := h.db.FinishShopModel3D(saveCtx, m.ProductID, version, failure); err != nil {
		log.Printf("modélisation %s: enregistrement: %v", label, err)
	}
}

func shopModel3DErrorMessage(err error) string {
	var apiErr *anthropic.Error
	switch {
	case errors.Is(err, scene3d.ErrRefused):
		return "Le modèle a refusé cette demande. Reformule la consigne ou change les photos."
	case errors.Is(err, scene3d.ErrTooLong):
		return "La scène est trop détaillée pour une seule réponse. Demande moins de détails."
	case errors.Is(err, context.DeadlineExceeded):
		return "La génération a pris trop de temps. Relance-la."
	case errors.As(err, &apiErr):
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "La clé ANTHROPIC_API_KEY du serveur est refusée."
		case http.StatusPaymentRequired:
			return "Le compte Anthropic n’a plus de crédit."
		case http.StatusTooManyRequests, 529:
			return "Le service de génération est saturé. Réessaie dans quelques minutes."
		case http.StatusRequestEntityTooLarge:
			return "Les photos sont trop lourdes pour une seule demande. Retires-en quelques-unes."
		}
		return "Le service de génération a renvoyé une erreur. Réessaie."
	}
	return "La génération a échoué. Réessaie."
}

// AdminUndoShopModel3D POST — revient à la version précédente de la scène.
func (h *Handlers) AdminUndoShopModel3D(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	done, err := h.db.UndoShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "retour impossible"})
		return
	}
	if !done {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "aucune version précédente disponible"})
		return
	}
	m, err := h.db.GetShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "modélisation indisponible"})
		return
	}
	h.writeShopModel3D(w, http.StatusOK, m)
}

// AdminPublishShopModel3D POST — montre la version actuelle sur la fiche de l'article.
func (h *Handlers) AdminPublishShopModel3D(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	m, err := h.db.GetShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "publication impossible"})
		return
	}
	if len(m.Versions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "aucun rendu à publier"})
		return
	}
	current := m.Versions[len(m.Versions)-1].Scene
	photos, err := shopModel3DScenePhotos(current, m.Photos)
	if err != nil {
		log.Printf("modélisation %s: publication: %v", p.ID.Hex(), err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "publication impossible"})
		return
	}
	if err := h.db.PublishShopModel3D(r.Context(), p.ID, current, photos); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "publication impossible"})
		return
	}
	m.Published = current
	m.PublishedPhotos = photos
	h.writeShopModel3D(w, http.StatusOK, m)
}

// AdminUnpublishShopModel3D DELETE — retire la vue 3D de la fiche, sans toucher au travail.
func (h *Handlers) AdminUnpublishShopModel3D(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	if err := h.db.UnpublishShopModel3D(r.Context(), p.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "retrait impossible"})
		return
	}
	m, err := h.db.GetShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "modélisation indisponible"})
		return
	}
	h.writeShopModel3D(w, http.StatusOK, m)
}

// shopModel3DScenePhotos : seules les photos reprises par une impression sont exposées au public.
func shopModel3DScenePhotos(raw string, photos []string) ([]string, error) {
	var scene scene3d.Scene
	if err := json.Unmarshal([]byte(raw), &scene); err != nil {
		return nil, err
	}
	used := []string{}
	for _, id := range photos {
		if slices.ContainsFunc(scene.Decals, func(d scene3d.Decal) bool { return strings.HasPrefix(id, d.PhotoID) }) {
			used = append(used, id)
		}
	}
	return used, nil
}

// PublicShopModel3D GET — scène de la vue 3D d'un article publié. Le résumé reste à l'admin : il décrit
// le travail de modélisation, pas le produit.
func (h *Handlers) PublicShopModel3D(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.GetShopProductBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil || !p.Active || !p.Model3DPublished {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "vue 3D introuvable"})
		return
	}
	m, err := h.db.GetShopModel3D(r.Context(), p.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	var scene scene3d.Scene
	if err := json.Unmarshal([]byte(m.Published), &scene); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "vue 3D introuvable"})
		return
	}
	scene.Summary = ""
	writeJSON(w, http.StatusOK, map[string]any{"scene": scene, "photos": m.PublishedPhotos})
}

// removeShopModel3D efface le travail d'un article supprimé.
func (h *Handlers) removeShopModel3D(ctx context.Context, m *models.ShopProduct) {
	photos, err := h.db.DeleteShopModel3D(ctx, m.ID)
	if err != nil {
		log.Printf("modélisation: suppression %s: %v", m.ID.Hex(), err)
		return
	}
	for _, id := range photos {
		h.removeShopMediaFile(id)
	}
}

// Garde-fou de compilation : le vrai client doit rester branchable.
var _ sceneGenerator = (*scene3d.Client)(nil)
