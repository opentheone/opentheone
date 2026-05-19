package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	openai "github.com/sashabaranov/go-openai"

	"github.com/opentheone/opentheone/backend/internal/model"
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

// ChatMessage is a simple role/content tuple plus optional tool-calling fields.
// The same struct is reused for every role in the agent loop:
//
//   - system / user: only Role + Content
//   - assistant calling tools: Role=assistant, ToolCalls populated, Content
//     may be empty
//   - tool result: Role=tool, ToolCallID = id of the call this satisfies,
//     Content = textual result the LLM should read
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Tool is an OpenAI-compatible tool descriptor. Parameters is a JSON Schema
// (raw JSON) so we pass it through verbatim from the MCP layer.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is one decided tool invocation by the assistant.
// ID is the OpenAI-assigned call id; we echo it back in the matching
// tool-result message.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string, as returned by the model
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
		Messages:    toOpenAIMessages(msgs),
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

// ChatTurn is one turn of the agent loop. Exactly one of:
//   - Content non-empty  → assistant produced a final user-visible reply
//   - ToolCalls non-empty → assistant wants to call tools; engine should
//     execute them and feed results back in the next turn.
//
// Both can technically be non-empty (model talks while requesting tools),
// in which case the engine should usually defer the text until tools resolve.
type ChatTurn struct {
	Content   string
	ToolCalls []ToolCall
	// FinishReason is the OpenAI-style reason: "stop", "tool_calls", "length", ...
	FinishReason string
}

// ChatWithTools runs one streaming chat completion with tool definitions
// and accumulates streaming tool-call fragments into whole ToolCall records.
//
// Why streaming: OpenAI returns tool-call arguments incrementally; the only
// reliable way to capture them in full (and to detect parallel tool calls)
// is to stream and concatenate by Index. We *also* stream regular content so
// the future "live typing" UI work can hook in here without another refactor;
// today we just collect the full text and return it once at the end.
//
// If tools is empty, behaviour matches Chat() but uses the streaming endpoint
// (still returns ChatTurn for a single code path in the engine).
func (c *Client) ChatWithTools(
	ctx context.Context,
	msgs []ChatMessage,
	tools []Tool,
) (ChatTurn, error) {
	if c.config == nil || c.config.ChatModel == "" {
		return ChatTurn{}, errors.New("llm: chat_model not configured")
	}
	req := openai.ChatCompletionRequest{
		Model:       c.config.ChatModel,
		Temperature: c.config.Temperature,
		MaxTokens:   c.config.MaxTokens,
		Messages:    toOpenAIMessages(msgs),
		Stream:      true,
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  json.RawMessage(t.Parameters),
			},
		})
	}

	stream, err := c.api.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return ChatTurn{}, fmt.Errorf("chat stream: %w", err)
	}
	defer stream.Close()

	var (
		buf          string
		finishReason string
		// tool-call accumulator: index → partial call
		callsByIndex = map[int]*ToolCall{}
		// preserve emission order so we feed them back in the order the model
		// declared them (mirrors how OpenAI numbers parallel calls).
		callOrder []int
	)

	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ChatTurn{}, fmt.Errorf("chat stream recv: %w", err)
		}
		if len(resp.Choices) == 0 {
			continue
		}
		choice := resp.Choices[0]
		if choice.Delta.Content != "" {
			buf += choice.Delta.Content
		}
		for _, tc := range choice.Delta.ToolCalls {
			// Some providers (notably Azure) omit Index; treat absent index
			// as 0 since they also don't support parallel calls.
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			acc, ok := callsByIndex[idx]
			if !ok {
				acc = &ToolCall{}
				callsByIndex[idx] = acc
				callOrder = append(callOrder, idx)
			}
			if tc.ID != "" {
				acc.ID = tc.ID
			}
			if tc.Function.Name != "" {
				acc.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.Arguments += tc.Function.Arguments
			}
		}
		if choice.FinishReason != "" {
			finishReason = string(choice.FinishReason)
		}
	}

	out := ChatTurn{
		Content:      buf,
		FinishReason: finishReason,
	}
	for _, idx := range callOrder {
		call := callsByIndex[idx]
		if call == nil {
			continue
		}
		// Some providers send the id only in the first chunk and the rest
		// rely on positional index; if id is still empty, synthesize a stable
		// one so the next round's tool message can reference it.
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d", idx)
		}
		// Arguments must be valid JSON — if the model produced nothing,
		// default to {} so the downstream parse doesn't blow up.
		if call.Arguments == "" {
			call.Arguments = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, *call)
	}
	return out, nil
}

// toOpenAIMessages converts our flat ChatMessage list into the SDK's
// per-role message variants. Tool messages MUST carry ToolCallID; assistant
// messages with ToolCalls must carry the matching call list.
func toOpenAIMessages(msgs []ChatMessage) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, 0, len(msgs))
	for _, m := range msgs {
		om := openai.ChatCompletionMessage{
			Role:    m.Role,
			Content: m.Content,
		}
		if m.Role == openai.ChatMessageRoleTool {
			om.ToolCallID = m.ToolCallID
		}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		out = append(out, om)
	}
	return out
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
