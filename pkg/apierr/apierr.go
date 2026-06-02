package apierr

import (
	"encoding/json"
	"net/http"
)

type Style int

const (
	StyleOpenAI Style = iota
	StyleAnthropic
)

type openaiEnvelope struct {
	Error openaiBody `json:"error"`
}

type openaiBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

type anthropicEnvelope struct {
	Type  string        `json:"type"`
	Error anthropicBody `json:"error"`
}

type anthropicBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func Write(w http.ResponseWriter, style Style, status int, msg, errType string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	switch style {
	case StyleAnthropic:
		_ = json.NewEncoder(w).Encode(anthropicEnvelope{
			Type:  "error",
			Error: anthropicBody{Type: errType, Message: msg},
		})
	default:
		_ = json.NewEncoder(w).Encode(openaiEnvelope{
			Error: openaiBody{Message: msg, Type: errType},
		})
	}
}
