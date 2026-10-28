package httptransport

import (
	"encoding/json"
	"net/http"
)

// ApiError represents the standard JSON error payload.
type ApiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ApiError{Code: code, Message: message})
}
