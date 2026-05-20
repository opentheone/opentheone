package llm

// ProviderPreset describes a built-in LLM provider template that the frontend
// can use to one-click prefill an LLMConfig form. Backend is the source of
// truth so we can extend the list without re-shipping the frontend.
//
// Embedding fields were intentionally removed: the memory system no longer
// uses vector embeddings — it relies on BM25 + LLM-driven hierarchical
// summarization (L0 → L1 → L2 → L3). Users only need a chat model.
type ProviderPreset struct {
	// ID is a stable identifier ("deepseek", "openai", ...).
	ID string `json:"id"`
	// Name is the display label ("DeepSeek").
	Name string `json:"name"`
	// BaseURL is the OpenAI-compatible base URL.
	BaseURL string `json:"base_url"`
	// ChatModel is the suggested default chat model.
	ChatModel string `json:"chat_model"`
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
			ID:        "deepseek",
			Name:      "DeepSeek",
			BaseURL:   "https://api.deepseek.com/v1",
			ChatModel: "deepseek-v4-pro",
			SignupURL: "https://platform.deepseek.com/",
			Note:      "国内访问稳定，价格低。推荐默认选择。",
		},
		{
			ID:        "openai",
			Name:      "OpenAI",
			BaseURL:   "https://api.openai.com/v1",
			ChatModel: "gpt-4o-mini",
			SignupURL: "https://platform.openai.com/",
			Note:      "原版 OpenAI，需要稳定境外网络。",
		},
		{
			ID:        "qwen",
			Name:      "通义千问 (Qwen)",
			BaseURL:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
			ChatModel: "qwen-plus",
			SignupURL: "https://bailian.console.aliyun.com/",
			Note:      "阿里云 DashScope OpenAI 兼容模式。",
		},
		{
			ID:        "kimi",
			Name:      "Kimi (Moonshot)",
			BaseURL:   "https://api.moonshot.cn/v1",
			ChatModel: "moonshot-v1-32k",
			SignupURL: "https://platform.moonshot.cn/",
			Note:      "Moonshot Kimi。长上下文。",
		},
		{
			ID:        "claude",
			Name:      "Claude（需 OpenAI 兼容代理）",
			BaseURL:   "https://api.example.com/v1",
			ChatModel: "claude-opus-4-7",
			SignupURL: "https://console.anthropic.com/",
			Note:      "Anthropic 原生不是 OpenAI 协议，需要前置代理（如 LiteLLM）做协议转换。",
		},
	}
}

// DefaultPreset returns the preset to use for a brand-new user.
func DefaultPreset() ProviderPreset {
	return Presets()[0]
}
