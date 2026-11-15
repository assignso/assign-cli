package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type canonicalReadBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *canonicalReadBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

// Ordinary canonical reads share one deadline and at most three attempts. Writes
// retain their existing original-command recovery and are never replayed here.
// Operation polling and acquisition/stream protocols keep their separate owners.
func (transport *operationTransport) canonicalExchange(request *http.Request) (*http.Response, error) {
	if (request.Method != http.MethodGet && request.Method != http.MethodHead) || (request.Body != nil && request.Body != http.NoBody) {
		return transport.client.Do(request)
	}
	now := transport.now
	if now == nil {
		now = time.Now
	}
	sleep := transport.sleep
	if sleep == nil {
		sleep = sleepOperation
	}
	jitter := transport.jitter
	if jitter == nil {
		jitter = func(cap time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(cap) + 1)) }
	}
	ctx, cancel := context.WithCancel(request.Context())
	deadline, bounded := ctx.Deadline()
	if !bounded {
		budget := 30 * time.Second
		if transport.client.Timeout > 0 {
			budget = min(budget, transport.client.Timeout)
		}
		cancel()
		ctx, cancel = context.WithTimeout(request.Context(), budget)
		deadline, _ = ctx.Deadline()
	}
	finish := func(response *http.Response, err error) (*http.Response, error) {
		if err != nil || response == nil {
			cancel()
			return response, err
		}
		// Body consumption belongs to the same deadline; returning headers must
		// not cancel the response before the existing bounded decoder reads it.
		response.Body = &canonicalReadBody{ReadCloser: response.Body, cancel: cancel}
		return response, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		response, err := transport.client.Do(request.Clone(ctx))
		transient := err != nil
		if response != nil {
			switch response.StatusCode {
			case 408, 429, 502, 503, 504:
				transient = true
			}
		}
		if !transient || attempt == 2 || ctx.Err() != nil {
			return finish(response, err)
		}
		delay := jitter(time.Duration(100*(1<<attempt)) * time.Millisecond)
		if response != nil {
			floor, valid := canonicalRetryAfter(response.Header, now())
			if !valid {
				return finish(response, err)
			}
			delay = max(delay, floor)
		}
		if delay >= deadline.Sub(now()) {
			return finish(response, err)
		}
		if response != nil {
			response.Body.Close()
		}
		if err := sleep(ctx, delay); err != nil {
			cancel()
			return nil, err
		}
	}
	panic("unreachable canonical recovery attempt")
}

func canonicalRetryAfter(header http.Header, now time.Time) (time.Duration, bool) {
	values := header.Values("Retry-After")
	if len(values) == 0 {
		return 0, true
	}
	if len(values) != 1 {
		return 0, false
	}
	raw := strings.TrimSpace(values[0])
	if seconds, err := strconv.ParseUint(raw, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(raw)
	return max(0, date.Sub(now)), err == nil
}

func (transport *operationTransport) exchangeJSON(ctx context.Context, method, path, operation string, body, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return newExitError(ExitOperation, "encode %s request", operation)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, transport.host+path, reader)
	if err != nil {
		return newExitError(ExitOperation, "prepare %s request", operation)
	}
	request.GetBody = nil
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Authorization", "Bearer "+transport.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := transport.canonicalExchange(request)
	if err != nil {
		return newExitError(ExitOperation, "request %s: %v", operation, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return newExitError(ExitAuthentication, "%s access denied (HTTP %d); check the credential and its scopes", operation, response.StatusCode)
	case http.StatusNotFound:
		return newExitError(ExitContext, "the requested %s is unavailable", operation)
	case http.StatusOK:
	default:
		return productReadError(response, operation)
	}
	const maxResponse = 2 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	encoding := response.Header.Get("Content-Encoding")
	if err != nil || len(data) > maxResponse || (encoding != "" && encoding != "identity") || json.Unmarshal(data, target) != nil {
		return newExitError(ExitOperation, "decode %s response", operation)
	}
	return nil
}
