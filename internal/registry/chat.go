package registry

// KnownChatModels lists the OpenAI-shaped chat models we surface in /v1/models
// for client discovery. mulerun routes them via /v1/chat/completions and
// /v1/messages, so we don't need to register vendor paths.
//
// Entries with a "vendor/" prefix in ID (e.g. "openai/gpt-5.5") use the
// code-plane routing path declared in ChatVendorPaths below. Prefix-less
// IDs go to the legacy /v1/chat/completions surface.
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
	// (see ChatVendorPaths). These are the GPT-5.x family `mulerun code`
	// exposes — useful for codex-style coding agents.
	{"openai/gpt-5.5", "openai"},
	{"openai/gpt-5.4-mini", "openai"},
	{"openai/gpt-5.4-nano", "openai"},
	{"openai/gpt-5.3-codex", "openai"},
}

// ChatVendorPaths maps a vendor prefix (the part before "/" in a chat model
// ID) to the upstream HTTP path that serves it. The chat handler looks up
// this map; clients see the prefixed ID via /v1/models.
//
// Single source of truth: when adding a vendor-prefixed model to
// KnownChatModels above, make sure its vendor exists here too. Otherwise
// the request silently falls back to /v1/chat/completions and the upstream
// returns "unknown model" — caught only at runtime.
var ChatVendorPaths = map[string]string{
	"openai": "/vendors/openai/v1/chat/completions",
	// Adding a vendor:
	//   1. Pick the path from agents.yaml (mulerun-cli) or reverse-engineer
	//      from `mulerun code` traffic.
	//   2. Verify with curl + a known model on muk- auth.
	//   3. Add the row here AND the prefixed model IDs above.
}

