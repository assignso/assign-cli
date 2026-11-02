package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCanonicalSafeReadRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, method, retryAfter string
		statuses                 []int
		calls                    int
		cancelBackoff            bool
	}{
		{name: "transient read then success", method: "GET", statuses: []int{503, 200}, calls: 2},
		{name: "third attempt stops", method: "HEAD", statuses: []int{503, 503, 503, 200}, calls: 3},
		{name: "Retry-After exceeds caller budget", method: "GET", statuses: []int{429, 200}, retryAfter: "3600", calls: 1},
		{name: "cancellation during backoff", method: "GET", statuses: []int{503, 200}, calls: 1, cancelBackoff: true},
		{name: "write never replayed", method: "POST", statuses: []int{503, 200}, calls: 1},
		{name: "denial never replayed", method: "GET", statuses: []int{401, 200}, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls, closed := 0, 0
			var deadline time.Time
			var active context.Context
			transport := &operationTransport{now: time.Now, jitter: func(time.Duration) time.Duration { return 0 }}
			transport.sleep = func(ctx context.Context, _ time.Duration) error {
				if closed != calls {
					t.Fatal("discarded response was not closed before recovery")
				}
				if tc.cancelBackoff {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			transport.client.Transport = operationFixtureRoundTrip(func(request *http.Request) (*http.Response, error) {
				calls++
				current, ok := request.Context().Deadline()
				if !ok || (!deadline.IsZero() && !deadline.Equal(current)) {
					t.Fatal("retry replaced the caller deadline")
				}
				deadline, active = current, request.Context()
				if request.Header.Get("Authorization") != "Bearer prepared" || request.Header.Get("If-Match") != `"opaque"` {
					t.Fatal("recovery changed request identity")
				}
				if calls > len(tc.statuses) {
					t.Fatal("unexpected retry")
				}
				headers := make(http.Header)
				if tc.retryAfter != "" {
					headers.Set("Retry-After", tc.retryAfter)
				}
				return &http.Response{StatusCode: tc.statuses[calls-1], Header: headers, Body: &recoveryFixtureBody{Reader: strings.NewReader("prepared"), close: func() { closed++ }}}, nil
			})
			request, err := http.NewRequestWithContext(ctx, tc.method, "https://prepared.invalid/api/v1/tasks/task", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer prepared")
			request.Header.Set("If-Match", `"opaque"`)
			response, err := transport.canonicalExchange(request)
			if calls != tc.calls {
				t.Fatalf("got%d attempts; want%d", calls, tc.calls)
			}
			if tc.cancelBackoff {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if active.Err() != nil {
				t.Fatal("returning response headers canceled body consumption")
			}
			data, err := io.ReadAll(response.Body)
			if err != nil || string(data) != "prepared" {
				t.Fatal("response body not preserved")
			}
			response.Body.Close()
			if tc.method != "POST" && active.Err() == nil {
				t.Fatal("closing response did not release its deadline")
			}
		})
	}
}

type recoveryFixtureBody struct {
	io.Reader
	close func()
}

func (body *recoveryFixtureBody) Close() error { body.close(); return nil }
