package strava

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"runapp/internal/models"
)

const (
	authorizeURL = "https://www.strava.com/oauth/authorize"
	tokenURL     = "https://www.strava.com/oauth/token"
	apiBase      = "https://www.strava.com/api/v3"
)

type Client struct {
	ClientID     string
	ClientSecret string
	HTTP         *http.Client
}

func New(clientID, clientSecret string) *Client {
	return &Client{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) AuthorizeURL(redirectURI, state, scope string) string {
	u, _ := url.Parse(authorizeURL)
	q := u.Query()
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("approval_prompt", "force")
	q.Set("scope", scope)
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String()
}

type tokenResponse struct {
	TokenType    string `json:"token_type"`
	ExpiresAt    int64  `json:"expires_at"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
}

func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (models.StravaTokens, error) {
	vals := url.Values{}
	vals.Set("client_id", c.ClientID)
	vals.Set("client_secret", c.ClientSecret)
	vals.Set("code", code)
	vals.Set("grant_type", "authorization_code")
	vals.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewBufferString(vals.Encode()))
	if err != nil {
		return models.StravaTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return models.StravaTokens{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return models.StravaTokens{}, fmt.Errorf("strava token exchange: %s: %s", resp.Status, string(body))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return models.StravaTokens{}, err
	}
	exp := time.Unix(tr.ExpiresAt, 0).UTC()
	if tr.ExpiresAt == 0 && tr.ExpiresIn > 0 {
		exp = time.Now().UTC().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return models.StravaTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    exp,
	}, nil
}

func (c *Client) Refresh(ctx context.Context, refresh string) (models.StravaTokens, error) {
	vals := url.Values{}
	vals.Set("client_id", c.ClientID)
	vals.Set("client_secret", c.ClientSecret)
	vals.Set("refresh_token", refresh)
	vals.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewBufferString(vals.Encode()))
	if err != nil {
		return models.StravaTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return models.StravaTokens{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return models.StravaTokens{}, fmt.Errorf("strava refresh: %s: %s", resp.Status, string(body))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return models.StravaTokens{}, err
	}
	exp := time.Unix(tr.ExpiresAt, 0).UTC()
	if tr.ExpiresAt == 0 && tr.ExpiresIn > 0 {
		exp = time.Now().UTC().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return models.StravaTokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    exp,
	}, nil
}

// ActivitiesSummary returns a compact JSON-friendly summary for the LLM.
func (c *Client) ActivitiesSummary(ctx context.Context, accessToken string, perPage int) ([]map[string]any, error) {
	if perPage <= 0 {
		perPage = 20
	}
	u := fmt.Sprintf("%s/athlete/activities?per_page=%d", apiBase, perPage)
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("strava activities: %s: %s", resp.Status, string(body))
	}

	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, len(raw))
	for _, act := range raw {
		distM := jsonFloat(act["distance"])
		movS := jsonFloat(act["moving_time"])
		elpS := jsonFloat(act["elapsed_time"])
		avgMs := jsonFloat(act["average_speed"])
		maxMs := jsonFloat(act["max_speed"])

		m := map[string]any{
			"name":                 act["name"],
			"type":                 act["type"],
			"start_date":           act["start_date"],
			"distance_km":          round2(distM / 1000),
			"moving_time_min":      round1(movS / 60),
			"elapsed_time_min":     round1(elpS / 60),
			"elevation_gain_m":     act["total_elevation_gain"],
			"average_speed_kmh":    round2(avgMs * 3.6),
			"max_speed_kmh":        round2(maxMs * 3.6),
			"average_heartrate":    act["average_heartrate"],
			"max_heartrate":        act["max_heartrate"],
		}
		// Allure moyenne (min/km) si course avec vitesse > 0
		if avgMs > 0 && distM >= 100 {
			m["pace_min_per_km"] = round2(1000 / (60 * avgMs))
		}
		out = append(out, m)
	}
	return out, nil
}

func jsonFloat(v any) float64 {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return 0
	}
}

func round1(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*10) / 10
}

func round2(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return math.Round(x*100) / 100
}
