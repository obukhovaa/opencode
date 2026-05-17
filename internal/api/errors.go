package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/opencode-ai/opencode/internal/logging"
)

// errEmptyBody is returned by readJSON when the request body is nil or empty.
var errEmptyBody = errors.New("request body is empty")

// isEmptyBodyError reports whether err originated from an empty request body
// (as opposed to malformed JSON or a read failure).
func isEmptyBodyError(err error) bool {
	return errors.Is(err, errEmptyBody)
}

// APIError represents a standard error response.
type APIError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logging.Error("failed to encode JSON response", "error", err)
	}
}

// writeError writes a standard error response.
func writeError(w http.ResponseWriter, status int, message string) {
	apiErr := APIError{
		Error:   http.StatusText(status),
		Message: message,
		Status:  status,
	}
	writeJSON(w, status, apiErr)
}

// readJSON decodes a JSON request body into the target.
func readJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return errEmptyBody
	}
	defer r.Body.Close()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}

	if len(body) == 0 {
		return errEmptyBody
	}

	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	return nil
}
