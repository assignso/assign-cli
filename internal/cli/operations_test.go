package cli

// Deterministic consumer/transport fixture source. Phase-deferred: these cases
// are not runtime or real Core/PostgreSQL integration evidence until executed
// after the full-product implementation checkpoint.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const operationFixtureID = "10000000-0000-4000-8000-000000000001"
const operationFixtureWorkspace = "10000000-0000-4000-8000-000000000002"
const operationFixtureAgent = "10000000-0000-4000-8000-000000000003"

func operationFixture(state string) string {
	value := map[string]any{"id": operationFixtureID, "state": state, "created_at": "2026-10-23T12:00:00Z", "updated_at": "2026-10-23T12:00:01Z", "expires_at": "2026-10-30T12:00:01Z"}
	switch state {
	case "pending", "running", "cancel_requested":
		value["outcome"], value["progress"] = "active", nil
	case "succeeded":
		value["outcome"] = "succeeded"
		value["result"] = map[string]any{"kind": "authorized-resource", "operation_id": "listWorkspaceAgentRunOutputs", "media_type": "application/json", "href": "/api/v1/workspaces/" + operationFixtureWorkspace + "/agents/" + operationFixtureAgent + "/runs/" + operationFixtureID + "/outputs"}
	case "failed":
		value["outcome"] = "failed"
		value["error"] = map[string]any{"type": "https://api.assign.so/problems/operation_failed", "title": "Operation failed", "status": 409, "code": "operation_failed"}
	case "cancelled":
		value["outcome"] = "cancelled"
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func TestHostedAgentAndOperationCommandsUseCanonicalSingleExchange(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-terminal-token")
	for _, action := range []string{"submit", "show", "cancel", "wait"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer prepared-terminal-token" || r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("If-Match") != "" || r.Header.Get("X-CSRF-Token") != "" || r.ProtoMajor != 1 || !r.Close {
					t.Errorf("unexpected custody or representation headers")
				}
				path, method, body, state, status := "/api/v1/operations/"+operationFixtureID, http.MethodGet, "", "succeeded", 200
				if action == "submit" {
					path, method, body, state, status = "/api/v1/workspaces/"+operationFixtureWorkspace+"/agents/"+operationFixtureAgent+"/runs", http.MethodPost, "{}", "pending", 202
					w.Header().Set("Location", "/api/v1/operations/"+operationFixtureID)
				}
				if action == "cancel" {
					path, method, state, status = path+"/cancel", http.MethodPost, "cancel_requested", 202
				}
				data, _ := io.ReadAll(r.Body)
				if r.URL.RequestURI() != path || r.Method != method || string(data) != body {
					t.Errorf("unexpected canonical request %s %s body=%q", r.Method, r.URL, data)
				}
				if (method == http.MethodPost) != (r.Header.Get("Idempotency-Key") == "prepared-operation-key-01") || (body == "{}") != (r.Header.Get("Content-Type") == "application/json") {
					t.Error("incorrect command key or body coding")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, operationFixture(state))
			}))
			defer server.Close()
			args := []string{"operation", action, operationFixtureID}
			if action == "submit" {
				args = []string{"agent", "run", operationFixtureAgent, "--workspace", operationFixtureWorkspace}
			}
			if action == "submit" || action == "cancel" {
				args = append(args, "--idempotency-key", "prepared-operation-key-01")
			}
			out, diagnostics, err := executeTaskFixture(t, server, "", args...)
			if err != nil || calls != 1 || !strings.Contains(out, operationFixtureID) || strings.Contains(out, "prepared-terminal-token") {
				t.Fatalf("out=%q diagnostics=%q err=%v calls=%d", out, diagnostics, err, calls)
			}
			if (action == "submit" || action == "cancel") && !strings.Contains(diagnostics, "prepared-operation-key-01") {
				t.Fatal("original key was not retained before the command")
			}
		})
	}
}

func TestOperationUnknownMutationNeverRetriesOrFollowsRecoveryLocation(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-terminal-token")
	for _, outcome := range []string{"lost-response", "mismatched-location", "conflict", "redirect"} {
		t.Run(outcome, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = io.Copy(io.Discard, r.Body)
				if outcome == "lost-response" {
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Location", "/api/v1/operations/"+operationFixtureID)
				switch outcome {
				case "conflict":
					w.WriteHeader(http.StatusConflict)
				case "redirect":
					w.WriteHeader(http.StatusTemporaryRedirect)
				case "mismatched-location":
					w.Header().Set("Location", "https://other.invalid/api/v1/operations/"+operationFixtureID)
					w.WriteHeader(http.StatusAccepted)
					_, _ = io.WriteString(w, operationFixture("pending"))
				}
			}))
			defer server.Close()
			out, diagnostics, err := executeTaskFixture(t, server, "", "agent", "run", operationFixtureAgent, "--workspace", operationFixtureWorkspace, "--idempotency-key", "prepared-operation-key-01")
			if err == nil || out != "" || calls != 1 || !strings.Contains(diagnostics, "prepared-operation-key-01") {
				t.Fatalf("out=%q diagnostics=%q err=%v calls=%d", out, diagnostics, err, calls)
			}
			if outcome == "conflict" && (exitCode(err) != ExitConflict || !strings.Contains(err.Error(), operationFixtureID)) {
				t.Fatal("existing authorized recovery identity was lost")
			}
		})
	}
}

type operationFixtureRoundTrip func(*http.Request) (*http.Response, error)

func (transport operationFixtureRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func preparedOperationTransport(t *testing.T, state func(int) (int, string, http.Header), deadline time.Duration) (*operationTransport, *[]time.Time, *[]time.Duration) {
	t.Helper()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	end := now.Add(deadline)
	calls, sleeps := []time.Time{}, []time.Duration{}
	transport := &operationTransport{host: "https://prepared.invalid", token: "prepared", now: func() time.Time { return now }, jitter: func(time.Duration) time.Duration { return 0 }}
	transport.client.Transport = operationFixtureRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/operations/"+operationFixtureID {
			t.Fatal("wait dispatched noncanonical work")
		}
		calls = append(calls, now)
		status, body, headers := state(len(calls))
		if headers == nil {
			headers = http.Header{}
		}
		headers.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	transport.sleep = func(ctx context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		if err := ctx.Err(); err != nil {
			return err
		}
		if !now.Add(delay).Before(end) {
			return context.DeadlineExceeded
		}
		now = now.Add(delay)
		return nil
	}
	return transport, &calls, &sleeps
}

func TestExternalOperationWaitOwnsSpacingJitterAndTerminalStop(t *testing.T) {
	transport, calls, sleeps := preparedOperationTransport(t, func(call int) (int, string, http.Header) {
		if call == 1 {
			return 429, "", http.Header{"Retry-After": {"5"}}
		}
		if call == 2 {
			return 200, operationFixture("running"), http.Header{}
		}
		return 200, operationFixture("succeeded"), nil
	}, time.Minute)
	transport.jitter = func(window time.Duration) time.Duration { return window / 2 }
	result, err := transport.wait(context.Background(), operationFixtureID)
	if err != nil || result.State != "succeeded" || len(*calls) != 3 || len(*sleeps) != 2 || (*sleeps)[0] != 5500*time.Millisecond || (*sleeps)[1] != 3*time.Second {
		t.Fatalf("state=%s err=%v calls=%v sleeps=%v", result.State, err, calls, sleeps)
	}
}

func TestExternalOperationWaitBoundsAttemptsDeadlineAndDenial(t *testing.T) {
	for _, mode := range []string{"attempts", "deadline", "date-floor", "ambiguous-retry", "malformed-retry", "denied", "expired", "cancelled-context", "terminal-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			transport, calls, _ := preparedOperationTransport(t, func(int) (int, string, http.Header) {
				switch mode {
				case "deadline":
					return 200, operationFixture("running"), http.Header{"Retry-After": {"61"}}
				case "date-floor":
					return 200, operationFixture("running"), http.Header{"Retry-After": {"Fri, 23 Oct 2026 12:01:01 GMT"}}
				case "ambiguous-retry":
					return 200, operationFixture("running"), http.Header{"Retry-After": {"2", "5"}}
				case "malformed-retry":
					return 200, operationFixture("running"), http.Header{"Retry-After": {"invalid"}}
				case "denied":
					return 403, "", nil
				case "expired":
					return 410, "", nil
				case "terminal-cancelled":
					return 200, operationFixture("cancelled"), nil
				default:
					return 200, operationFixture("running"), nil
				}
			}, time.Minute)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled-context" {
				cancel()
			}
			result, err := transport.wait(ctx, operationFixtureID)
			wantCalls := 1
			if mode == "attempts" {
				wantCalls = 30
			}
			if mode == "cancelled-context" {
				wantCalls = 0
			}
			if len(*calls) != wantCalls || ((mode == "terminal-cancelled") != (err == nil)) {
				t.Fatalf("err=%v calls=%d result=%s", err, len(*calls), result.State)
			}
			if (mode == "deadline" || mode == "date-floor") && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("lost deadline cause")
			}
			for index := 1; index < len(*calls); index++ {
				if (*calls)[index].Sub((*calls)[index-1]) < 2*time.Second {
					t.Fatal("status requests too frequent")
				}
			}
		})
	}
}

func TestCompactOperationRejectsAmbiguityWrongIdentityAndUnsafeResults(t *testing.T) {
	valid := operationFixture("succeeded")
	for _, body := range []string{
		strings.Replace(valid, operationFixtureID, operationFixtureAgent, 1),
		strings.Replace(valid, `"state":"succeeded"`, `"state":"succeeded","st\u0061te":"failed"`, 1),
		strings.Replace(valid, `"state":"succeeded"`, `"State":"succeeded"`, 1),
		strings.Replace(valid, "/api/v1/workspaces/", "https://other.invalid/api/v1/workspaces/", 1),
		strings.Replace(valid, "/runs/"+operationFixtureID, "/runs/"+operationFixtureID+"?token=unsafe", 1),
		strings.Replace(valid, `"listWorkspaceAgentRunOutputs"`, `"arbitraryOperation"`, 1),
		valid + "{}",
		strings.Repeat(" ", operationStatusLimit) + valid,
		strings.Replace(operationFixture("running"), `"progress":null`, `"progress":2`, 1),
		string([]byte{0xff}),
	} {
		if _, err := decodeOperation([]byte(body), operationFixtureID); err == nil {
			t.Fatalf("accepted invalid status: %.120s", body)
		}
	}
	if _, err := decodeOperation([]byte(valid), operationFixtureID); err != nil {
		t.Fatal(err)
	}
}

func TestExternalOperationWaitStopsAnInflightReadOnCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport, calls, _ := preparedOperationTransport(t, func(int) (int, string, http.Header) {
		t.Fatal("unexpected prepared response")
		return 0, "", nil
	}, time.Minute)
	requests := 0
	transport.client.Transport = operationFixtureRoundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	_, err := transport.wait(ctx, operationFixtureID)
	if !errors.Is(err, context.Canceled) || requests != 1 || len(*calls) != 0 {
		t.Fatalf("err=%v explicit attempts=%d", err, requests)
	}
}

// Source-authored only: these independent canonical response fixtures remain
// phase-deferred with the existing transport harness. No provider is involved.
func TestOperationRecoverySupportsEveryIntegratedSourceWithoutFetchingResults(t *testing.T) {
	t.Setenv("ASSIGN_TOKEN", "prepared-terminal-token")
	for _, source := range []struct{ name, reader, path string }{
		{"agent", "listWorkspaceAgentRunOutputs", "/api/v1/workspaces/" + operationFixtureWorkspace + "/agents/" + operationFixtureAgent + "/runs/" + operationFixtureID + "/outputs"},
		{"lifecycle", "getLifecycleMigration", "/api/v1/workspaces/" + operationFixtureWorkspace + "/lifecycle-migrations/" + operationFixtureID},
		{"knowledge", "getWorkspaceKnowledgeAnswer", "/api/v1/workspaces/" + operationFixtureWorkspace + "/knowledge/answers/" + operationFixtureID},
		// A repository command is not its result sync run. This distinct source UUID
		// prevents imposing the Agent run identity relation on every source family.
		{"repository", "getKnowledgeRepositorySyncRun", "/api/v1/workspaces/" + operationFixtureWorkspace + "/knowledge-repository/runs/" + operationFixtureAgent},
		{"work-change-set", "getChangeSetOperationResult", "/api/v1/workspaces/" + operationFixtureWorkspace + "/change-set-operations/" + operationFixtureID + "/result"},
	} {
		for _, action := range []string{"show", "cancel", "wait"} {
			t.Run(source.name+"/"+action, func(t *testing.T) {
				var value map[string]any
				if err := json.Unmarshal([]byte(operationFixture("succeeded")), &value); err != nil {
					t.Fatal(err)
				}
				value["result"] = map[string]any{"kind": "authorized-resource", "operation_id": source.reader, "href": source.path, "media_type": "application/json"}
				body, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					method := http.MethodGet
					path := "/api/v1/operations/" + operationFixtureID
					if action == "cancel" {
						method = http.MethodPost
						path += "/cancel"
					}
					if r.Method != method || r.URL.Path != path || r.URL.RawQuery != "" {
						t.Errorf("unexpected dispatch: %s %s", r.Method, r.URL)
					}
					if action == "cancel" && r.Header.Get("Idempotency-Key") != "prepared-operation-cancel-01" {
						t.Error("original cancellation key was lost")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(body)
				}))
				defer server.Close()
				args := []string{"operation", action, operationFixtureID}
				if action == "cancel" {
					args = append(args, "--idempotency-key", "prepared-operation-cancel-01")
				}
				out, _, err := executeTaskFixture(t, server, "", args...)
				if err != nil || calls != 1 || !strings.Contains(out, source.reader) || !strings.Contains(out, source.path) {
					t.Fatalf("out=%q err=%v calls=%d", out, err, calls)
				}
			})
		}
	}
}

func TestOperationSourceReferencesRefuseUnsafeOrCrossBoundResults(t *testing.T) {
	prefix := "/api/v1/workspaces/" + operationFixtureWorkspace
	for _, ref := range []cliOperationResult{
		{"authorized-resource", "getLifecycleMigration", prefix + "/lifecycle-migrations/" + operationFixtureAgent, "application/json"},
		{"authorized-resource", "getChangeSetOperationResult", prefix + "/change-set-operations/" + operationFixtureAgent + "/result", "application/json"},
		{"authorized-resource", "getWorkspaceKnowledgeAnswer", prefix + "/knowledge/answers/" + operationFixtureAgent, "application/json"},
		{"authorized-resource", "getKnowledgeRepositorySyncRun", prefix + "/knowledge-repository/runs/00000000-0000-0000-0000-000000000000", "application/json"},
		{"authorized-resource", "getWorkspaceKnowledgeAnswer", prefix + "/lifecycle-migrations/" + operationFixtureID, "application/json"},
		{"authorized-resource", "getKnowledgeRepositorySyncRun", "https://other.invalid" + prefix + "/knowledge-repository/runs/" + operationFixtureAgent, "application/json"},
		{"authorized-resource", "getKnowledgeRepositorySyncRun", prefix + "/knowledge-repository/runs/" + operationFixtureAgent + "?token=unsafe", "application/json"},
		{"authorized-resource", "getWorkspaceKnowledgeAnswer", prefix + "/knowledge/answers/%31" + operationFixtureID[1:], "application/json"},
		{"authorized-resource", "getLifecycleMigration", prefix + "/lifecycle-migrations/../" + operationFixtureID, "application/json"},
		{"authorized-resource", "getLifecycleMigration", prefix + "/lifecycle-migrations/" + operationFixtureID + "#result", "application/json"},
		{"authorized-resource", "getWorkspaceKnowledgeAnswer", prefix + "/knowledge/answers/" + operationFixtureID, "text/html"},
	} {
		var value map[string]any
		if err := json.Unmarshal([]byte(operationFixture("succeeded")), &value); err != nil {
			t.Fatal(err)
		}
		value["result"] = ref
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeOperation(body, operationFixtureID); err == nil {
			t.Errorf("accepted unsafe result: %s %s", ref.OperationID, ref.Href)
		}
	}
}
