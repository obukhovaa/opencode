package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/opencode-ai/opencode/internal/session"
)

func sessionPageOptions(r *http.Request) (session.PageOptions, error) {
	query := r.URL.Query()
	options := session.PageOptions{Cursor: query.Get("cursor"), Search: query.Get("search")}
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return options, errors.New("limit must be a positive integer")
		}
		options.Limit = n
	}
	if raw := query.Get("roots"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return options, errors.New("roots must be true or false")
		}
		options.RootsOnly = value
	}
	if len(options.Search) > 1024 {
		return options, errors.New("search is too long")
	}
	return options, nil
}
func sessionPageHeaders(w http.ResponseWriter, r *http.Request, next string) {
	if next == "" {
		return
	}
	url := *r.URL
	query := url.Query()
	query.Set("cursor", next)
	url.RawQuery = query.Encode()
	w.Header().Set("Link", "<"+url.RequestURI()+">; rel=\"next\"")
	w.Header().Set("X-Next-Cursor", next)
	w.Header().Add("Access-Control-Expose-Headers", "Link, X-Next-Cursor")
}
func writeSessionPageError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrInvalidCursor) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to list sessions")
}
