// Package push envoie des notifications via le service Expo Push (relais vers APNs / FCM).
// Aucun certificat Apple n’est manipulé ici : l’app fournit un jeton « ExponentPushToken[...] »
// et Expo se charge de la livraison avec la clé APNs configurée sur le projet EAS.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	expoSendURL = "https://exp.host/--/api/v2/push/send"
	// L’API Expo refuse au-delà de 100 messages par requête.
	maxBatch = 100
)

type Client struct {
	http *http.Client
	// accessToken : requis seulement si « Enhanced Security for Push Notifications » est activé
	// sur le compte Expo. Vide sinon.
	accessToken string
}

func New(accessToken string) *Client {
	return &Client{
		http:        &http.Client{Timeout: 15 * time.Second},
		accessToken: strings.TrimSpace(accessToken),
	}
}

// Message : une notification pour un jeton donné.
type Message struct {
	To       string         `json:"to"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Sound    string         `json:"sound,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Priority string         `json:"priority,omitempty"`
	// Badge : pastille sur l’icône iOS.
	Badge *int `json:"badge,omitempty"`
}

type ticket struct {
	Status  string `json:"status"`
	ID      string `json:"id"`
	Message string `json:"message"`
	Details struct {
		Error string `json:"error"`
	} `json:"details"`
}

type sendResponse struct {
	Data   []ticket `json:"data"`
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// ErrNotConfigured : aucun jeton à notifier.
var ErrNotConfigured = errors.New("push: aucun destinataire")

// IsExpoToken filtre les jetons manifestement invalides avant l’appel réseau.
func IsExpoToken(t string) bool {
	t = strings.TrimSpace(t)
	return strings.HasPrefix(t, "ExponentPushToken[") || strings.HasPrefix(t, "ExpoPushToken[")
}

// Send envoie le même message à tous les jetons et renvoie ceux qui doivent être supprimés
// (appareil désinstallé / jeton révoqué → DeviceNotRegistered).
func (c *Client) Send(ctx context.Context, tokens []string, m Message) (invalid []string, err error) {
	valid := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if IsExpoToken(t) {
			valid = append(valid, t)
		} else {
			invalid = append(invalid, t)
		}
	}
	if len(valid) == 0 {
		return invalid, ErrNotConfigured
	}

	for start := 0; start < len(valid); start += maxBatch {
		end := min(start+maxBatch, len(valid))
		batch := valid[start:end]
		dead, err := c.sendBatch(ctx, batch, m)
		if err != nil {
			return invalid, err
		}
		invalid = append(invalid, dead...)
	}
	return invalid, nil
}

func (c *Client) sendBatch(ctx context.Context, tokens []string, m Message) ([]string, error) {
	msgs := make([]Message, 0, len(tokens))
	for _, t := range tokens {
		cp := m
		cp.To = t
		if cp.Sound == "" {
			cp.Sound = "default"
		}
		if cp.Priority == "" {
			cp.Priority = "high"
		}
		msgs = append(msgs, cp)
	}
	body, err := json.Marshal(msgs)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, expoSendURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var parsed sendResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		if res.StatusCode >= 300 {
			return nil, fmt.Errorf("expo push: HTTP %d", res.StatusCode)
		}
		return nil, err
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("expo push: %s (%s)", parsed.Errors[0].Message, parsed.Errors[0].Code)
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("expo push: HTTP %d", res.StatusCode)
	}

	// Les tickets suivent l’ordre des messages envoyés.
	var dead []string
	for i, t := range parsed.Data {
		if t.Status == "ok" || i >= len(tokens) {
			continue
		}
		if t.Details.Error == "DeviceNotRegistered" {
			dead = append(dead, tokens[i])
		}
	}
	return dead, nil
}
