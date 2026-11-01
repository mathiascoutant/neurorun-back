package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"runapp/internal/models"

	"github.com/go-chi/chi/v5"
)

const (
	shopMaxImageBytes int64 = 15 << 20
	shopMaxVideoBytes int64 = 200 << 20
	shopMaxMedia            = 20
)

// shopMediaName : le nom vient de la route publique, il ne doit pas pouvoir sortir du dossier.
var shopMediaName = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|png|webp|mp4|webm)$`)

// shopAllowedMedia : le type est déduit du contenu du fichier, jamais de son nom ni de ce que
// déclare le navigateur. Le SVG est exclu : servi tel quel, il pourrait exécuter du script.
var shopAllowedMedia = map[string]struct {
	Ext  string
	Kind string
}{
	"image/jpeg": {".jpg", models.ShopMediaImage},
	"image/png":  {".png", models.ShopMediaImage},
	"image/webp": {".webp", models.ShopMediaImage},
	"video/mp4":  {".mp4", models.ShopMediaVideo},
	"video/webm": {".webm", models.ShopMediaVideo},
}

func (h *Handlers) removeShopMediaFile(id string) {
	if !shopMediaName.MatchString(id) {
		return
	}
	if err := os.Remove(filepath.Join(h.cfg.ShopMediaDir, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("boutique: suppression média %s: %v", id, err)
	}
}

// AdminUploadShopMedia POST multipart — ajoute une image ou une vidéo à la fin de la galerie.
// Lecture en flux : une vidéo de 200 Mo ne doit pas tenir en mémoire.
func (h *Handlers) AdminUploadShopMedia(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	if len(p.Media) >= shopMaxMedia {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "20 médias maximum par article"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, shopMaxVideoBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "envoi invalide"})
		return
	}
	var part io.Reader
	for {
		pt, err := mr.NextPart()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fichier manquant ou trop lourd"})
			return
		}
		if pt.FormName() == "file" {
			part = pt
			break
		}
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "fichier illisible"})
		return
	}
	head = head[:n]
	contentType := http.DetectContentType(head)
	allowed, ok := shopAllowedMedia[contentType]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "format non pris en charge : JPEG, PNG, WebP, MP4 ou WebM"})
		return
	}
	limit := shopMaxImageBytes
	if allowed.Kind == models.ShopMediaVideo {
		limit = shopMaxVideoBytes
	}

	if err := os.MkdirAll(h.cfg.ShopMediaDir, 0o755); err != nil {
		log.Printf("boutique: dossier médias: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stockage indisponible"})
		return
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stockage indisponible"})
		return
	}
	id := hex.EncodeToString(raw) + allowed.Ext
	dest := filepath.Join(h.cfg.ShopMediaDir, id)
	tmp, err := os.CreateTemp(h.cfg.ShopMediaDir, "upload-*")
	if err != nil {
		log.Printf("boutique: fichier temporaire: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stockage indisponible"})
		return
	}
	defer os.Remove(tmp.Name())

	written, err := io.Copy(tmp, io.LimitReader(io.MultiReader(bytes.NewReader(head), part), limit+1))
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "envoi interrompu ou fichier trop lourd"})
		return
	}
	if written > limit {
		msg := "image trop lourde (15 Mo maximum)"
		if allowed.Kind == models.ShopMediaVideo {
			msg = "vidéo trop lourde (200 Mo maximum)"
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		log.Printf("boutique: droits média: %v", err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		log.Printf("boutique: enregistrement média: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stockage indisponible"})
		return
	}

	media := models.ShopMedia{ID: id, Kind: allowed.Kind, ContentType: contentType}
	if err := h.db.AddShopMedia(r.Context(), p.ID, media); err != nil {
		h.removeShopMediaFile(id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusCreated, media)
}

// AdminDeleteShopMedia DELETE — retire un média de la galerie et du disque. Un article publié garde
// au moins une image : sans elle, il ne pourrait plus s’afficher au catalogue.
func (h *Handlers) AdminDeleteShopMedia(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	mediaID := chi.URLParam(r, "mediaId")
	left := make([]models.ShopMedia, 0, len(p.Media))
	found := false
	for _, m := range p.Media {
		if m.ID == mediaID {
			found = true
			continue
		}
		left = append(left, m)
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "média introuvable"})
		return
	}
	if p.Active && shopFirstImage(left) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "un article publié doit garder au moins une image"})
		return
	}
	if err := h.db.RemoveShopMedia(r.Context(), p.ID, mediaID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	h.removeShopMediaFile(mediaID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PublicShopMedia GET — sert un média. `ServeContent` gère les requêtes partielles, indispensables
// aux vidéos ; le nom étant unique et le fichier jamais modifié, le cache peut durer.
func (h *Handlers) PublicShopMedia(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "file")
	if !shopMediaName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(h.cfg.ShopMediaDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
