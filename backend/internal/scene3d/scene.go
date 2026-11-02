package scene3d

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
)

// Le modèle ne produit pas de fichier 3D : il décrit une scène avec les formes ci-dessous, et le
// navigateur la construit avec three.js. Une description plutôt que du code : rien de ce que renvoie
// le modèle n'est exécuté dans la page d'administration.
const (
	maxParts    = 120
	maxDecals   = 24
	maxPoints   = 64
	maxSections = 64
)

var (
	colorHex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	shapes   = map[string]bool{"box": true, "sphere": true, "cylinder": true, "cone": true, "torus": true, "lathe": true, "loft": true, "extrude": true}
	finishes = map[string]bool{"plain": true, "knit": true, "mesh": true, "leather": true, "brushed": true}
	lights   = map[string]bool{"studio": true, "soft": true, "dramatic": true, "daylight": true}
)

type Scene struct {
	Summary    string     `json:"summary"`
	Background Background `json:"background"`
	Floor      string     `json:"floor"`
	Lighting   string     `json:"lighting"`
	Camera     Camera     `json:"camera"`
	Parts      []Part     `json:"parts"`
	Decals     []Decal    `json:"decals"`
}

type Background struct {
	Top    string `json:"top"`
	Bottom string `json:"bottom"`
}

type Camera struct {
	Distance float64 `json:"distance"`
	Height   float64 `json:"height"`
	FOV      float64 `json:"fov"`
}

type Part struct {
	Name       string      `json:"name"`
	Shape      string      `json:"shape"`
	Dimensions []float64   `json:"dimensions"`
	Points     [][]float64 `json:"points"`
	Sections   [][]float64 `json:"sections"`
	Capped     bool        `json:"capped"`
	Position   []float64   `json:"position"`
	Rotation   []float64   `json:"rotation"`
	Material   Material    `json:"material"`
}

type Material struct {
	Color     string  `json:"color"`
	Roughness float64 `json:"roughness"`
	Metalness float64 `json:"metalness"`
	Opacity   float64 `json:"opacity"`
	Finish    string  `json:"finish"`
}

type Decal struct {
	PhotoID          string    `json:"photo_id"`
	Crop             []float64 `json:"crop"`
	Part             string    `json:"part"`
	Position         []float64 `json:"position"`
	Normal           []float64 `json:"normal"`
	Width            float64   `json:"width"`
	Rotation         float64   `json:"rotation"`
	RemoveBackground bool      `json:"remove_background"`
}

// ParseScene lit la réponse du modèle et écarte ce que le navigateur ne saurait pas dessiner : une
// pièce mal formée ne doit pas faire perdre toute une génération. photoIDs : préfixes acceptés pour
// les impressions, qui ne peuvent venir que des photos fournies.
func ParseScene(raw string, photoIDs []string) (*Scene, error) {
	var s Scene
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	s.Summary = strings.TrimSpace(s.Summary)
	if !colorHex.MatchString(s.Background.Top) {
		s.Background.Top = "#1a1a1d"
	}
	if !colorHex.MatchString(s.Background.Bottom) {
		s.Background.Bottom = s.Background.Top
	}
	if s.Floor != "none" {
		s.Floor = "shadow"
	}
	if !lights[s.Lighting] {
		s.Lighting = "studio"
	}
	s.Camera.FOV = clamp(s.Camera.FOV, 15, 70)
	s.Camera.Distance = clamp(s.Camera.Distance, 0, 100)
	s.Camera.Height = clamp(s.Camera.Height, -50, 50)

	parts := make([]Part, 0, len(s.Parts))
	names := make(map[string]bool, len(s.Parts))
	for _, p := range s.Parts {
		if len(parts) == maxParts {
			break
		}
		if !validPart(&p) {
			continue
		}
		parts = append(parts, p)
		names[p.Name] = true
	}
	if len(parts) == 0 {
		return nil, errors.New("scène sans pièce exploitable")
	}
	s.Parts = parts

	decals := make([]Decal, 0, len(s.Decals))
	for _, d := range s.Decals {
		if len(decals) == maxDecals {
			break
		}
		if validDecal(&d, names, photoIDs) {
			decals = append(decals, d)
		}
	}
	s.Decals = decals
	return &s, nil
}

func validPart(p *Part) bool {
	if p.Name == "" || !shapes[p.Shape] || !finite(p.Dimensions) {
		return false
	}
	if !vec3(p.Position) || !vec3(p.Rotation) {
		return false
	}
	need := map[string]int{"box": 3, "sphere": 3, "cylinder": 3, "cone": 2, "torus": 2}[p.Shape]
	if len(p.Dimensions) < need {
		return false
	}
	switch p.Shape {
	case "lathe", "extrude":
		if len(p.Points) < 2 || len(p.Points) > maxPoints || !rows(p.Points, 2) {
			return false
		}
		if p.Shape == "extrude" && (len(p.Points) < 3 || len(p.Dimensions) < 1) {
			return false
		}
		p.Sections = [][]float64{}
	case "loft":
		if len(p.Sections) < 2 || len(p.Sections) > maxSections || !rows(p.Sections, 5) {
			return false
		}
		p.Points = [][]float64{}
	}
	m := &p.Material
	if !colorHex.MatchString(m.Color) {
		m.Color = "#808080"
	}
	if !finishes[m.Finish] {
		m.Finish = "plain"
	}
	m.Roughness = clamp(m.Roughness, 0, 1)
	m.Metalness = clamp(m.Metalness, 0, 1)
	m.Opacity = clamp(m.Opacity, 0.05, 1)
	return true
}

func validDecal(d *Decal, parts map[string]bool, photoIDs []string) bool {
	if !parts[d.Part] || !vec3(d.Position) || !vec3(d.Normal) || len(d.Crop) != 4 || !finite(d.Crop) {
		return false
	}
	if d.Width <= 0 || !finite([]float64{d.Width, d.Rotation}) || d.Normal[0] == 0 && d.Normal[1] == 0 && d.Normal[2] == 0 {
		return false
	}
	x, y := clamp(d.Crop[0], 0, 1), clamp(d.Crop[1], 0, 1)
	w, h := math.Min(d.Crop[2], 1-x), math.Min(d.Crop[3], 1-y)
	if w <= 0 || h <= 0 {
		return false
	}
	d.Crop = []float64{x, y, w, h}
	if len(d.PhotoID) < 8 {
		return false
	}
	for _, id := range photoIDs {
		if strings.HasPrefix(id, d.PhotoID) {
			return true
		}
	}
	return false
}

func vec3(v []float64) bool { return len(v) == 3 && finite(v) }

func rows(list [][]float64, size int) bool {
	for _, row := range list {
		if len(row) != size || !finite(row) {
			return false
		}
	}
	return true
}

func finite(list []float64) bool {
	for _, v := range list {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func clamp(v, low, high float64) float64 {
	return math.Max(low, math.Min(high, v))
}
