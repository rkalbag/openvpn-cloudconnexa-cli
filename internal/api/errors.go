package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Sentinel errors for errors.Is on an *APIError.
var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrRateLimited  = errors.New("rate limited")
	ErrServer       = errors.New("server error")
)

const maxMessageLen = 300

// APIError is a non-2xx API response.
type APIError struct {
	Method     string
	Path       string // without query string
	Status     int
	Message    string // server message, sanitised and truncated; may be empty
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if h := e.Hint(); h != "" {
		b.WriteString(" (" + h + ")")
	}
	return b.String()
}

// Unwrap lets errors.Is match the sentinel for the status class.
func (e *APIError) Unwrap() error {
	switch {
	case e.Status == http.StatusUnauthorized:
		return ErrUnauthorized
	case e.Status == http.StatusForbidden:
		return ErrForbidden
	case e.Status == http.StatusNotFound:
		return ErrNotFound
	case e.Status == http.StatusConflict:
		return ErrConflict
	case e.Status == http.StatusTooManyRequests:
		return ErrRateLimited
	case e.Status >= 500:
		return ErrServer
	}
	return nil
}

// Hint is a short user-facing suggestion for the error, if any.
func (e *APIError) Hint() string {
	switch {
	case e.Status == http.StatusUnauthorized:
		return "credentials rejected or expired; re-run `ccx login`"
	case e.Status == http.StatusForbidden:
		return "check the plan is Essential or higher and the API credential has the needed permissions"
	case e.Status == http.StatusNotFound:
		return "it may have been deleted; see `ccx ls --all`"
	case e.Status == http.StatusConflict:
		return "a resource with that name or domain may already exist"
	case e.Status == http.StatusTooManyRequests:
		return "rate limited by CloudConnexa; wait a minute and retry"
	case e.Status >= 500:
		return "CloudConnexa returned a server error; retry later"
	}
	return ""
}

// errorResponse is v1.json's ErrorResponse.
type errorResponse struct {
	ErrorMessage string                       `json:"errorMessage"`
	StatusError  string                       `json:"statusError"`
	ErrorDetails map[string][]errorDetailItem `json:"errorDetails"`
}

type errorDetailItem struct {
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

func newAPIError(method, path string, status int, body []byte, retryAfter time.Duration) *APIError {
	return &APIError{Method: method, Path: path, Status: status, Message: errorMessage(body), RetryAfter: retryAfter}
}

// errorMessage extracts a message from an ErrorResponse body. Non-JSON
// bodies are ignored so arbitrary server output never reaches the user.
func errorMessage(body []byte) string {
	var er errorResponse
	if len(body) == 0 || json.Unmarshal(body, &er) != nil {
		return ""
	}
	parts := []string{}
	if er.ErrorMessage != "" {
		parts = append(parts, er.ErrorMessage)
	}
	fields := make([]string, 0, len(er.ErrorDetails))
	for f := range er.ErrorDetails {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		for _, d := range er.ErrorDetails[f] {
			msg := d.Message
			if msg == "" {
				msg = d.ErrorCode
			}
			if msg != "" {
				parts = append(parts, f+": "+msg)
			}
		}
	}
	return sanitize(strings.Join(parts, "; "))
}

// sanitize strips control characters and truncates.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxMessageLen {
		s = string(r[:maxMessageLen]) + "…"
	}
	return strings.TrimSpace(s)
}
