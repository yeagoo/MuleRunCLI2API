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
	{"claude-sonnet-4-6", "anthropic"},
	{"claude-haiku-4-5", "anthropic"},
	{"claude-opus-4-7", "anthropic"},
}
