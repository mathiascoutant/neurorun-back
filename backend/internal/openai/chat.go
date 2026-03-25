package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

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

func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	body := chatRequest{
		Model: c.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
	}
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

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", fmt.Errorf("openai decode: %w; body=%s", err, string(respBody))
	}
	if cr.Error != nil {
		return "", fmt.Errorf("openai: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("openai: no choices")
	}
	return cr.Choices[0].Message.Content, nil
}
