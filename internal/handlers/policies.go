package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type policiesResponse struct {
	Policies any `json:"policies"`
}

func NewPoliciesHandler(policies PolicyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(policiesResponse{Policies: policies.List()}); err != nil {
			slog.Error("failed to encode policies response", "error", err)
		}
	}
}
