package registry

// KnownChatModels lists the OpenAI-shaped chat models we surface in /v1/models
// for client discovery. mulerun routes them via /v1/chat/completions and
// /v1/messages, so we don't need to register vendor paths.
var KnownChatModels = []struct {
	ID     string
	Vendor string
}{
	{"gpt-5", "openai"},
	{"gpt-5-mini", "openai"},
	{"gpt-5-nano", "openai"},
	{"gpt-4.1", "openai"},
	{"gpt-4.1-mini", "openai"},
	{"o4-mini", "openai"},
	{"deepseek-v4-flash", "deepseek"},
	{"deepseek-v4-pro", "deepseek"},
	{"claude-sonnet-4-6", "anthropic"},
	{"claude-haiku-4-5", "anthropic"},
	{"claude-opus-4-7", "anthropic"},
	// Vendor-prefixed models route through the code-plane surface
	// (/vendors/openai/v1/chat/completions). These are the GPT-5.x family
	// `mulerun code` exposes — useful for codex-style coding agents.
	{"openai/gpt-5.5", "openai"},
	{"openai/gpt-5.4-mini", "openai"},
	{"openai/gpt-5.4-nano", "openai"},
	{"openai/gpt-5.3-codex", "openai"},
}
