// Package appstore lit les rapports de ventes de l’App Store Connect API.
//
// Ce que ces rapports disent, et ce qu’ils ne diront jamais : des totaux. Apple ne communique
// aucune identité, aucun identifiant d’appareil, rien qui permette de relier un téléchargement
// à une personne. Le plus fin qu’on obtienne est « n installations, tel jour, tel pays ».
package appstore

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrNoReport : Apple n’a pas (ou pas encore) de rapport pour ce jour. Ce n’est pas une panne —
// les rapports paraissent avec un jour de décalage, et un jour sans aucune vente n’existe pas.
var ErrNoReport = errors.New("aucun rapport pour cette date")

// Codes « Product Type Identifier » du rapport SALES.
// Référence : App Store Connect Help → Reference → Product type identifiers.
var (
	firstInstallTypes = map[string]bool{"1": true, "1F": true, "1T": true, "1-B": true}
	redownloadTypes   = map[string]bool{"3": true, "3F": true}
	updateTypes       = map[string]bool{"7": true, "7F": true, "7T": true}
)

// DayReport : un jour de rapport, réduit à ce qui nous intéresse.
type DayReport struct {
	Day string `json:"day"` // AAAA-MM-JJ
	// FirstInstalls : premiers téléchargements. C’est « le » chiffre de téléchargements.
	FirstInstalls int64 `json:"first_installs"`
	// Redownloads : réinstallations par quelqu’un qui avait déjà l’app.
	Redownloads int64 `json:"redownloads"`
	Updates     int64 `json:"updates"`
	// UnitsByType : totaux bruts par code produit, tels que lus dans le TSV. Permet de vérifier
	// le classement ci-dessus au lieu de le croire — les codes d’Apple évoluent.
	UnitsByType map[string]int64 `json:"units_by_type"`
	// InstallsByCountry : premiers téléchargements par code pays ISO.
	InstallsByCountry map[string]int64 `json:"installs_by_country"`
}

// Client interroge l’App Store Connect API avec une clé d’équipe.
type Client struct {
	issuerID     string
	keyID        string
	key          *ecdsa.PrivateKey
	vendorNumber string
	version      string
	http         *http.Client
}

// New construit le client. `keyP8` est le contenu PEM du fichier .p8 téléchargé à la création
// de la clé — il ne transite jamais ailleurs que vers api.appstoreconnect.apple.com.
func New(issuerID, keyID string, keyP8 []byte, vendorNumber, version string) (*Client, error) {
	key, err := jwt.ParseECPrivateKeyFromPEM(keyP8)
	if err != nil {
		return nil, fmt.Errorf("clé .p8 illisible : %w", err)
	}
	if version == "" {
		version = "1_1"
	}
	return &Client{
		issuerID:     issuerID,
		keyID:        keyID,
		key:          key,
		vendorNumber: vendorNumber,
		version:      version,
		http:         &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// token signe un JWT ES256 de courte durée. Apple refuse au-delà de 20 minutes ; on reste bien
// en deçà, le jeton ne servant qu’à l’appel en cours.
func (c *Client) token() (string, error) {
	now := time.Now()
	t := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": c.issuerID,
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"aud": "appstoreconnect-v1",
	})
	t.Header["kid"] = c.keyID
	return t.SignedString(c.key)
}

const salesReportsURL = "https://api.appstoreconnect.apple.com/v1/salesReports"

// DailySummary récupère le rapport SALES/SUMMARY d’un jour (UTC Apple) et l’agrège.
func (c *Client) DailySummary(ctx context.Context, day string) (DayReport, error) {
	tok, err := c.token()
	if err != nil {
		return DayReport{}, err
	}

	q := url.Values{}
	q.Set("filter[frequency]", "DAILY")
	q.Set("filter[reportType]", "SALES")
	q.Set("filter[reportSubType]", "SUMMARY")
	q.Set("filter[vendorNumber]", c.vendorNumber)
	q.Set("filter[reportDate]", day)
	q.Set("filter[version]", c.version)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, salesReportsURL+"?"+q.Encode(), nil)
	if err != nil {
		return DayReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/a-gzip")

	res, err := c.http.Do(req)
	if err != nil {
		return DayReport{}, err
	}
	defer res.Body.Close()

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return DayReport{}, ErrNoReport
	default:
		return DayReport{}, appleError(res)
	}

	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		return DayReport{}, fmt.Errorf("rapport illisible (gzip) : %w", err)
	}
	defer gz.Close()

	report, err := ParseSalesTSV(gz)
	if err != nil {
		return DayReport{}, err
	}
	report.Day = day
	return report, nil
}

// appleError remonte le message d’Apple tel quel.
//
// Ces erreurs sont presque toujours actionnables — mauvaise version de rapport, numéro de
// vendeur inconnu, clé sans le bon rôle — et le texte d’Apple les nomme précisément. Le
// remplacer par un « erreur App Store » générique obligerait à sortir les logs pour rien.
func appleError(res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	var parsed struct {
		Errors []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Errors) > 0 {
		e := parsed.Errors[0]
		msg := strings.TrimSpace(e.Detail)
		if msg == "" {
			msg = strings.TrimSpace(e.Title)
		}
		if msg != "" {
			return fmt.Errorf("App Store Connect (%d) : %s", res.StatusCode, msg)
		}
	}
	return fmt.Errorf("App Store Connect a répondu %d", res.StatusCode)
}

// ParseSalesTSV agrège un rapport SALES/SUMMARY déjà décompressé.
//
// Les colonnes sont repérées par leur en-tête, jamais par leur position : Apple en ajoute au fil
// des versions de rapport, et un index en dur se décalerait silencieusement.
func ParseSalesTSV(r io.Reader) (DayReport, error) {
	out := DayReport{
		UnitsByType:       map[string]int64{},
		InstallsByCountry: map[string]int64{},
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	var idxType, idxUnits, idxCountry = -1, -1, -1
	header := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if header {
			for i, name := range cols {
				switch strings.TrimSpace(name) {
				case "Product Type Identifier":
					idxType = i
				case "Units":
					idxUnits = i
				case "Country Code":
					idxCountry = i
				}
			}
			header = false
			if idxType < 0 || idxUnits < 0 {
				return out, errors.New("rapport inattendu : colonnes « Product Type Identifier » ou « Units » absentes")
			}
			continue
		}
		if idxType >= len(cols) || idxUnits >= len(cols) {
			continue
		}
		productType := strings.TrimSpace(cols[idxType])
		units, err := strconv.ParseInt(strings.TrimSpace(cols[idxUnits]), 10, 64)
		if err != nil {
			continue
		}
		out.UnitsByType[productType] += units
		switch {
		case firstInstallTypes[productType]:
			out.FirstInstalls += units
			if idxCountry >= 0 && idxCountry < len(cols) {
				if cc := strings.TrimSpace(cols[idxCountry]); cc != "" {
					out.InstallsByCountry[cc] += units
				}
			}
		case redownloadTypes[productType]:
			out.Redownloads += units
		case updateTypes[productType]:
			out.Updates += units
		}
	}
	return out, sc.Err()
}
