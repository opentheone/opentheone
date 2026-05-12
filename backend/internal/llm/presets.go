package llm

// ProviderPreset describes a built-in LLM provider template that the frontend
// can use to one-click prefill an LLMConfig form. Backend is the source of
// truth so we can extend the list without re-shipping the frontend.
type ProviderPreset struct {
	// ID is a stable identifier ("deepseek", "openai", ...).
	ID string `json:"id"`
	// Name is the display label ("DeepSeek").
	Name string `json:"name"`
	// BaseURL is the OpenAI-compatible base URL.
	BaseURL string `json:"base_url"`
	// ChatModel is the suggested default chat model.
	ChatModel string `json:"chat_model"`
	// EmbeddingModel is the suggested default embedding model.
	// Empty string means the provider does not offer embeddings; the memory
	// service will automatically fall back to recency-only ordering.
	EmbeddingModel string `json:"embedding_model"`
	// SupportsEmbedding signals whether the provider has an OpenAI-compatible
	// embedding API.
	SupportsEmbedding bool `json:"supports_embedding"`
	// SignupURL is a help link pointing the user to where to grab an API key.
	SignupURL string `json:"signup_url"`
	// Note is a one-line tip rendered next to the preset button.
	Note string `json:"note"`
}

// Presets returns the built-in provider catalog.
// DeepSeek is intentionally listed first and used as the default for new users.
func Presets() []ProviderPreset {
	return []ProviderPreset{
		{
			ID:                "deepseek",
			Name:              "DeepSeek",
			BaseURL:           "https://api.deepseek.com/v1",
			ChatModel:         "deepseek-v4-pro",
			EmbeddingModel:    "",
			SupportsEmbedding: false,
			SignupURL:         "https://platform.deepseek.com/",
			Note:              "国内访问稳定，价格低。无 embedding，TA 的长期记忆会自动退化为最近优先。",
		},
		{
			ID:                "openai",
			Name:              "OpenAI",
			BaseURL:           "https://api.openai.com/v1",
			ChatModel:         "gpt-4o-mini",
			EmbeddingModel:    "text-embedding-3-small",
			SupportsEmbedding: true,
			SignupURL:         "https://platform.openai.com/",
			Note:              "原版 OpenAI，需要稳定境外网络。",
		},
		{
			ID:                "qwen",
			Name:              "通义千问 (Qwen)",
			BaseURL:           "https://dashscope.aliyuncs.com/compatible-mode/v1",
			ChatModel:         "qwen-plus",
			EmbeddingModel:    "text-embedding-v3",
			SupportsEmbedding: true,
			SignupURL:         "https://bailian.console.aliyun.com/",
			Note:              "阿里云 DashScope OpenAI 兼容模式。",
		},
		{
			ID:                "kimi",
			Name:              "Kimi (Moonshot)",
			BaseURL:           "https://api.moonshot.cn/v1",
			ChatModel:         "moonshot-v1-32k",
			EmbeddingModel:    "",
			SupportsEmbedding: false,
			SignupURL:         "https://platform.moonshot.cn/",
			Note:              "Moonshot Kimi。长上下文。无 embedding。",
		},
		{
			ID:                "claude",
			Name:              "Claude（需 OpenAI 兼容代理）",
			BaseURL:           "https://api.example.com/v1",
			ChatModel:         "claude-opus-4-7",
			EmbeddingModel:    "",
			SupportsEmbedding: false,
			SignupURL:         "https://console.anthropic.com/",
			Note:              "Anthropic 原生不是 OpenAI 协议，需要前置代理（如 LiteLLM）做协议转换。",
		},
	}
}

// DefaultPreset returns the preset to use for a brand-new user.
func DefaultPreset() ProviderPreset {
	return Presets()[0]
}
