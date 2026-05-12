package llm

import (
	"context"
	"errors"
	"fmt"

	openai "github.com/sashabaranov/go-openai"

	"github.com/wzyjerry/opentheone/backend/internal/model"
)

// Client wraps go-openai with config and exposes Chat + Embed.
type Client struct {
	api    *openai.Client
	config *model.LLMConfig
}

// NewClient builds a Client from a decrypted config (APIKey filled in).
func NewClient(cfg *model.LLMConfig, apiKey string) *Client {
	conf := openai.DefaultConfig(apiKey)
	if cfg.BaseURL != "" {
		conf.BaseURL = cfg.BaseURL
	}
	return &Client{
		api:    openai.NewClientWithConfig(conf),
		config: cfg,
	}
}

// ChatMessage is a simple role/content tuple.
type ChatMessage struct {
	Role    string `json:"role"` // system / user / assistant
	Content string `json:"content"`
}

// Chat runs a chat-completion turn and returns the assistant text.
func (c *Client) Chat(ctx context.Context, msgs []ChatMessage) (string, error) {
	if c.config == nil || c.config.ChatModel == "" {
		return "", errors.New("llm: chat_model not configured")
	}
	req := openai.ChatCompletionRequest{
		Model:       c.config.ChatModel,
		Temperature: c.config.Temperature,
		MaxTokens:   c.config.MaxTokens,
	}
	for _, m := range msgs {
		req.Messages = append(req.Messages, openai.ChatCompletionMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	resp, err := c.api.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("llm: empty completion")
	}
	return resp.Choices[0].Message.Content, nil
}

// Embed turns one text into a float32 vector using the configured embedding_model.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	if c.config == nil || c.config.EmbeddingModel == "" {
		return nil, errors.New("llm: embedding_model not configured")
	}
	resp, err := c.api.CreateEmbeddings(ctx, openai.EmbeddingRequest{
		Model: openai.EmbeddingModel(c.config.EmbeddingModel),
		Input: []string{text},
	})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, errors.New("llm: empty embedding")
	}
	return resp.Data[0].Embedding, nil
}

// Ping does a quick test request used by /api/llm/test.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Chat(ctx, []ChatMessage{
		{Role: "system", Content: "You are a connectivity probe. Reply OK."},
		{Role: "user", Content: "ping"},
	})
	return err
}
