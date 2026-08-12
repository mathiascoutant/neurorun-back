package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	APIKey string
	Model  string
	HTTP   *http.Client
}

func New(apiKey, model string) *Client {
	return &Client{
		APIKey: apiKey,
		Model:  model,
		HTTP:   &http.Client{Timeout: 120 * time.Second},
	}
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls : actions demandées par le modèle (message d'assistant).
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID : identifiant de l'action à laquelle ce message répond (rôle "tool").
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type chatMessage = ChatMessage

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat envoie un unique tour system + user (rétrocompatible).
func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	return c.ChatMessages(ctx, []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	})
}

// ChatMessages envoie une liste complète de messages (system en premier typiquement).
func (c *Client) ChatMessages(ctx context.Context, messages []ChatMessage) (string, error) {
	cr, err := c.postCompletion(ctx, chatRequest{Model: c.Model, Messages: messages})
	if err != nil {
		return "", err
	}
	return cr.Choices[0].Message.Content, nil
}

// postCompletion envoie une requête /chat/completions et garantit au moins un choix.
func (c *Client) postCompletion(ctx context.Context, body any) (*chatResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("openai decode: %w; body=%s", err, string(respBody))
	}
	if cr.Error != nil {
		return nil, fmt.Errorf("openai: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices")
	}
	return &cr, nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ChatMessagesStream diffuse la réponse au fil de sa génération : `onDelta` reçoit
// chaque fragment de texte dès qu'il arrive, et le texte complet est renvoyé à la fin.
//
// C'est ce qui permet de suivre une génération longue sur des faits — ce qui est
// écrit — au lieu de deviner une progression à partir du temps écoulé.
func (c *Client) ChatMessagesStream(
	ctx context.Context,
	messages []ChatMessage,
	onDelta func(string),
) (string, error) {
	body := struct {
		chatRequest
		Stream bool `json:"stream"`
	}{chatRequest{Model: c.Model, Messages: messages}, true}

	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("openai stream: %s: %s", resp.Status, string(errBody))
	}

	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	// Une ligne SSE peut porter un fragment long : le tampon par défaut (64 ko) suffit,
	// on le double par prudence sur les réponses denses.
	sc.Buffer(make([]byte, 0, 64*1024), 512*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return "", fmt.Errorf("openai: %s", chunk.Error.Message)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content == "" {
				continue
			}
			full.WriteString(ch.Delta.Content)
			if onDelta != nil {
				onDelta(ch.Delta.Content)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if full.Len() == 0 {
		return "", fmt.Errorf("openai: réponse vide")
	}
	return full.String(), nil
}
