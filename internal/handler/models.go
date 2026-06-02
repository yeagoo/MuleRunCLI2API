package handler

import (
	"encoding/json"
	"net/http"

	"github.com/openmule/cli2api/internal/registry"
)

type modelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type modelListResponse struct {
	Object string      `json:"object"`
	Data   []modelItem `json:"data"`
}

// Models returns GET /v1/models — discoverable model catalog.
func Models(_ Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := make([]modelItem, 0, 64)
		for _, m := range registry.All() {
			data = append(data, modelItem{ID: m.ID, Object: "model", OwnedBy: "mulerun:" + m.Vendor})
		}
		for _, m := range registry.KnownChatModels {
			data = append(data, modelItem{ID: m.ID, Object: "model", OwnedBy: "mulerun:" + m.Vendor})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(modelListResponse{Object: "list", Data: data})
	}
}
