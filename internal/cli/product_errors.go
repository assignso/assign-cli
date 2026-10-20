package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode"
)

const maxProblemBytes = 64 * 1024

// productReadError uses the HTTP status as authority even when the response
// contains an inconsistent Problem Details status or comes from an HTML proxy.
func productReadError(response *http.Response, operation string) error {
	code := ExitOperation
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return newExitError(ExitAuthentication, "authentication was rejected; create or configure a current personal API token")
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = ExitArguments
	case http.StatusConflict, http.StatusPreconditionFailed:
		code = ExitConflict
	}
	message := fmt.Sprintf("%s request failed with HTTP %d", operation, response.StatusCode)
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/problem+json") {
		return newExitError(code, "%s", message)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProblemBytes+1))
	if err != nil || len(body) > maxProblemBytes {
		return newExitError(code, "%s", message)
	}
	var problem struct {
		Title  string `json:"title"`
		Status int    `json:"status"`
	}
	if json.Unmarshal(body, &problem) != nil || (problem.Status != 0 && problem.Status != response.StatusCode) {
		return newExitError(code, "%s", message)
	}
	// Never render arbitrary terminal controls, multiline payloads, or an
	// unbounded title from a remote error response.
	title := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, problem.Title)
	title = strings.Join(strings.Fields(title), " ")
	if runes := []rune(title); len(runes) > 200 {
		title = string(runes[:200])
	}
	if title != "" {
		message += ": " + title
	}
	return newExitError(code, "%s", message)
}
