// Package httptransport contains all HTTP routing and handler logic for CacheCraft.
//
// This file contains the response-writing utility functions used by all handlers.
// Instead of duplicating json.Encode logic in every handler, we centralize it here.
//
// In Go, you write an HTTP response by:
//   1. Setting headers with w.Header().Set("Key", "Value")
//   2. Writing the status code with w.WriteHeader(http.StatusOK)
//   3. Writing the body with w.Write([]byte{...}) or json.NewEncoder(w).Encode(v)
//
// Order matters: headers must be set BEFORE WriteHeader, and WriteHeader must
// be called BEFORE writing the body. Once you call w.Write(), the status code
// is implicitly sent as 200 if WriteHeader was not called.
//
// ApiError is the standardized JSON error response body, matching the Java version:
//   { "code": "ITEM_NOT_FOUND", "message": "item not found" }
package httptransport

import (
	"encoding/json"
	"net/http"
)

// ApiError is the JSON body returned for all error responses.
// It mirrors the ApiError record from the Java Spring Boot version.
type ApiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeJSON serializes value as JSON and writes it to the response with the
// given HTTP status code and Content-Type: application/json header.
//
// The http.ResponseWriter "w" is an interface. You call Header(), WriteHeader(),
// then the body is written by json.NewEncoder(w).Encode(v), which calls w.Write()
// internally. Using the encoder directly avoids allocating a temporary byte slice.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// json.NewEncoder(w) creates an encoder that writes directly into the response body.
	// .Encode(value) serializes value to JSON and appends a newline character.
	json.NewEncoder(w).Encode(value) //nolint:errcheck
}

// writeError writes a standardized ApiError JSON response.
// Used by all handlers to return consistent error shapes.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ApiError{Code: code, Message: message})
}
