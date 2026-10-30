package strava

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"

	"runapp/internal/models"
)

// Envoi des courses NeuroRun sur Strava (droit activity:write).
//
// Une course avec trace part en fichier TCX : Strava y retrouve le tracé, l'altitude
// et la fréquence cardiaque, et recalcule lui-même ses segments. Une course sans GPS
// (tapis, montre sans signal) devient une activité manuelle : distance et durée seules.

// UploadResult : ce que Strava dit d'un fichier envoyé. ActivityID reste à 0 tant que
// le traitement n'est pas fini.
type UploadResult struct {
	ID         int64
	ActivityID int64
	Status     string
	Error      string
}

// ErrUploadDuplicate : Strava a déjà cette activité (synchro Garmin Connect ou Santé,
// envoi précédent). DuplicateOf porte son identifiant quand Strava le donne.
type ErrUploadDuplicate struct{ DuplicateOf int64 }

func (e *ErrUploadDuplicate) Error() string {
	return fmt.Sprintf("strava: doublon de l'activité %d", e.DuplicateOf)
}

var duplicateRe = regexp.MustCompile(`(?i)duplicate of(?: activity)?\s*(?:<a[^>]*>)?\s*(\d+)`)

// uploadErrorAsDuplicate reconnaît le message « … duplicate of activity 123 » que
// Strava renvoie dans le champ error d'un envoi en double.
func uploadErrorAsDuplicate(msg string) (*ErrUploadDuplicate, bool) {
	m := duplicateRe.FindStringSubmatch(msg)
	if m == nil {
		return nil, false
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return &ErrUploadDuplicate{DuplicateOf: id}, true
}

type uploadResponse struct {
	ID         int64   `json:"id"`
	ActivityID *int64  `json:"activity_id"`
	Status     string  `json:"status"`
	Error      *string `json:"error"`
}

func (r uploadResponse) result() (UploadResult, error) {
	out := UploadResult{ID: r.ID, Status: r.Status}
	if r.ActivityID != nil {
		out.ActivityID = *r.ActivityID
	}
	if r.Error != nil && *r.Error != "" {
		out.Error = *r.Error
		if dup, ok := uploadErrorAsDuplicate(*r.Error); ok {
			return out, dup
		}
		return out, fmt.Errorf("strava upload: %s", *r.Error)
	}
	return out, nil
}

// UploadTCX envoie le fichier ; Strava répond tout de suite, l'activité vient après
// (voir GetUpload). externalID évite qu'un réessai crée une seconde activité.
func (c *Client) UploadTCX(ctx context.Context, accessToken string, tcx []byte, name, description, externalID string) (UploadResult, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fields := map[string]string{
		"data_type":   "tcx",
		"name":        name,
		"description": description,
		"external_id": externalID,
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return UploadResult{}, err
		}
	}
	fw, err := mw.CreateFormFile("file", externalID+".tcx")
	if err != nil {
		return UploadResult{}, err
	}
	if _, err := fw.Write(tcx); err != nil {
		return UploadResult{}, err
	}
	if err := mw.Close(); err != nil {
		return UploadResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/uploads", &buf)
	if err != nil {
		return UploadResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.doUpload(req, "strava upload")
}

// GetUpload relit l'état d'un envoi.
func (c *Client) GetUpload(ctx context.Context, accessToken string, uploadID int64) (UploadResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/uploads/%d", apiBase, uploadID), nil)
	if err != nil {
		return UploadResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return c.doUpload(req, "strava upload status")
}

func (c *Client) doUpload(req *http.Request, what string) (UploadResult, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return UploadResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		// Un doublon peut aussi revenir en erreur HTTP, avec le même message.
		var ur uploadResponse
		if json.Unmarshal(body, &ur) == nil && ur.Error != nil {
			if dup, ok := uploadErrorAsDuplicate(*ur.Error); ok {
				return UploadResult{ID: ur.ID, Error: *ur.Error}, dup
			}
		}
		return UploadResult{}, apiError(what, resp, body)
	}
	var ur uploadResponse
	if err := json.Unmarshal(body, &ur); err != nil {
		return UploadResult{}, err
	}
	return ur.result()
}

// WaitUpload relit l'envoi jusqu'à ce que Strava ait créé l'activité ou l'ait refusée.
// Strava traite un fichier en quelques secondes et demande de ne pas relire plus d'une
// fois par seconde.
func (c *Client) WaitUpload(ctx context.Context, accessToken string, first UploadResult) (UploadResult, error) {
	res := first
	delays := []time.Duration{2 * time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second, 8 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second}
	for _, d := range delays {
		if res.ActivityID != 0 {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(d):
		}
		var err error
		res, err = c.GetUpload(ctx, accessToken, first.ID)
		if err != nil {
			return res, err
		}
	}
	if res.ActivityID != 0 {
		return res, nil
	}
	return res, errors.New("strava upload: toujours en traitement")
}

// ManualActivity : course sans trace GPS.
type ManualActivity struct {
	Name        string
	Description string
	StartLocal  time.Time // heure locale du coureur
	ElapsedSec  int
	DistanceM   float64
}

// CreateManualActivity crée une activité course sans fichier et rend son identifiant.
func (c *Client) CreateManualActivity(ctx context.Context, accessToken string, a ManualActivity) (int64, error) {
	vals := url.Values{}
	vals.Set("name", a.Name)
	vals.Set("sport_type", "Run")
	vals.Set("start_date_local", a.StartLocal.Format("2006-01-02T15:04:05"))
	vals.Set("elapsed_time", strconv.Itoa(a.ElapsedSec))
	vals.Set("distance", strconv.FormatFloat(a.DistanceM, 'f', 1, 64))
	vals.Set("description", a.Description)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/activities", bytes.NewBufferString(vals.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return 0, apiError("strava create activity", resp, body)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// RenameActivity reporte sur Strava le nom donné à la course dans NeuroRun.
func (c *Client) RenameActivity(ctx context.Context, accessToken string, activityID int64, name string) error {
	payload, _ := json.Marshal(map[string]string{"name": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/activities/%d", apiBase, activityID), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return apiError("strava rename activity", resp, body)
	}
	return nil
}

// RunActivitiesBetween : les courses Strava parties entre after et before — de quoi
// voir si la montre a déjà synchronisé la sortie par un autre chemin.
func (c *Client) RunActivitiesBetween(ctx context.Context, accessToken string, after, before time.Time) ([]RunActivity, error) {
	u := fmt.Sprintf("%s/athlete/activities?per_page=30&after=%d&before=%d", apiBase, after.Unix(), before.Unix())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, apiError("strava activities between", resp, body)
	}
	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make([]RunActivity, 0, len(raw))
	for _, m := range raw {
		if ra, ok := mapToRunActivity(m); ok {
			out = append(out, ra)
		}
	}
	return out, nil
}

// minTCXPoints : en dessous, pas de tracé digne de ce nom — activité manuelle.
const minTCXPoints = 2

// HasTCXTrack : la course a assez de points GPS datés pour partir en fichier.
func HasTCXTrack(lr *models.LiveRun) bool {
	return len(tcxPoints(lr.TrackPoints)) >= minTCXPoints
}

// tcxPoints : points datés et positionnés, dans l'ordre du temps, sans horodatage
// répété (Strava refuse un fichier dont le temps recule).
func tcxPoints(track []models.LiveRunTrackPoint) []models.LiveRunTrackPoint {
	pts := make([]models.LiveRunTrackPoint, 0, len(track))
	for _, p := range track {
		if p.TMs <= 0 || (p.Lat == 0 && p.Lng == 0) || math.IsNaN(p.Lat) || math.IsNaN(p.Lng) {
			continue
		}
		pts = append(pts, p)
	}
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].TMs < pts[j].TMs })
	out := pts[:0]
	for _, p := range pts {
		if len(out) > 0 && p.TMs <= out[len(out)-1].TMs {
			continue
		}
		out = append(out, p)
	}
	return out
}

// BuildTCX écrit la course au format Garmin TCX. La distance cumulée suit le tracé
// mais est ramenée au total NeuroRun, pour que Strava affiche le même chiffre que l'app
// (la distance de la montre ne colle jamais tout à fait au GPS brut).
func BuildTCX(lr *models.LiveRun) []byte {
	pts := tcxPoints(lr.TrackPoints)
	cum := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		cum[i] = cum[i-1] + haversineM(pts[i-1].Lat, pts[i-1].Lng, pts[i].Lat, pts[i].Lng)
	}
	scale := 1.0
	if n := len(cum); n > 0 && cum[n-1] > 0 && lr.DistanceM > 0 {
		scale = lr.DistanceM / cum[n-1]
	}
	total := lr.DistanceM
	if total <= 0 && len(cum) > 0 {
		total = cum[len(cum)-1]
	}

	start := time.UnixMilli(pts[0].TMs).UTC()
	totalSec := lr.MovingSec
	if totalSec <= 0 {
		totalSec = float64(pts[len(pts)-1].TMs-pts[0].TMs) / 1000
	}

	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<TrainingCenterDatabase xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2">` + "\n")
	b.WriteString(" <Activities>\n  <Activity Sport=\"Running\">\n")
	fmt.Fprintf(&b, "   <Id>%s</Id>\n", start.Format(time.RFC3339))
	fmt.Fprintf(&b, "   <Lap StartTime=\"%s\">\n", start.Format(time.RFC3339))
	fmt.Fprintf(&b, "    <TotalTimeSeconds>%.1f</TotalTimeSeconds>\n", totalSec)
	fmt.Fprintf(&b, "    <DistanceMeters>%.1f</DistanceMeters>\n", total)
	b.WriteString("    <Calories>0</Calories>\n    <Intensity>Active</Intensity>\n    <TriggerMethod>Manual</TriggerMethod>\n    <Track>\n")
	for i, p := range pts {
		b.WriteString("     <Trackpoint>\n")
		fmt.Fprintf(&b, "      <Time>%s</Time>\n", time.UnixMilli(p.TMs).UTC().Format("2006-01-02T15:04:05.000Z"))
		fmt.Fprintf(&b, "      <Position><LatitudeDegrees>%.7f</LatitudeDegrees><LongitudeDegrees>%.7f</LongitudeDegrees></Position>\n", p.Lat, p.Lng)
		if p.AltM != nil && !math.IsNaN(*p.AltM) {
			fmt.Fprintf(&b, "      <AltitudeMeters>%.1f</AltitudeMeters>\n", *p.AltM)
		}
		fmt.Fprintf(&b, "      <DistanceMeters>%.1f</DistanceMeters>\n", cum[i]*scale)
		if p.HrBpm != nil && *p.HrBpm > 0 && *p.HrBpm < 260 {
			fmt.Fprintf(&b, "      <HeartRateBpm><Value>%d</Value></HeartRateBpm>\n", int(math.Round(*p.HrBpm)))
		}
		b.WriteString("     </Trackpoint>\n")
	}
	b.WriteString("    </Track>\n   </Lap>\n  </Activity>\n </Activities>\n</TrainingCenterDatabase>\n")
	return b.Bytes()
}

func haversineM(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLng := (lng2 - lng1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}
