package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

const changeSetResultLimit = 513 * 1024

func newChangeSetResultCommand(opts *options, client *http.Client) *cobra.Command {
	var workspace string
	command := &cobra.Command{
		Use:   "change-set-result <operation-id>",
		Short: "Read terminal outcomes for an existing Work ChangeSet Operation",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := operationID(args[0])
			if err != nil {
				return err
			}
			workspaceID, err := operationID(workspace)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), operationDeadline)
			defer cancel()
			transport, err := newOperationTransport(ctx, opts, client)
			if err != nil {
				return err
			}
			defer transport.client.CloseIdleConnections()
			result, err := transport.changeSetResult(ctx, workspaceID, id)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	command.Flags().StringVar(&workspace, "workspace", "", "Original Workspace UUID (required)")
	return command
}

func (transport *operationTransport) changeSetResult(ctx context.Context, workspace, id string) (json.RawMessage, error) {
	reader := operationResultReaders["getChangeSetOperationResult"]
	path := strings.NewReplacer("{workspace_id}", workspace, "{operation_id}", id).Replace(reader.Path)
	response, err := transport.requestChangeSet(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, newExitError(ExitOperation, "Could not read ChangeSet outcomes; retain the original Operation ID and key")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, newExitError(ExitAuthentication, "ChangeSet outcomes require access with the original credential")
	case http.StatusNotFound, http.StatusGone:
		return nil, newExitError(ExitContext, "ChangeSet outcomes are unavailable; retain the original Operation ID and key")
	case http.StatusConflict:
		return nil, newExitError(ExitConflict, "ChangeSet outcomes are not available yet; inspect the original Operation")
	case http.StatusOK:
	default:
		return nil, newExitError(ExitOperation, "ChangeSet outcome read returned HTTP %d", response.StatusCode)
	}
	data, err := readChangeSetResponse(response)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result struct {
		OperationID string            `json:"operation_id"`
		ID          string            `json:"id"`
		Version     string            `json:"version"`
		State       string            `json:"state"`
		Outcomes    []json.RawMessage `json:"outcomes"`
	}
	if unambiguousOperationJSON(decoder, 0) != nil || json.Unmarshal(data, &result) != nil {
		return nil, newExitError(ExitOperation, "Invalid ChangeSet outcomes")
	}
	proposalID, idErr := operationID(result.ID)
	terminal := result.State == "succeeded" || result.State == "failed" || result.State == "cancelled"
	if result.OperationID != id || idErr != nil || proposalID != result.ID || changeSetVersion(result.Version) == 0 ||
		!terminal || len(result.Outcomes) == 0 || len(result.Outcomes) > 100 {
		return nil, newExitError(ExitOperation, "ChangeSet outcomes do not match the original Operation")
	}
	return json.RawMessage(data), nil
}

func (transport *operationTransport) requestChangeSet(ctx context.Context, method, path string, payload []byte, key string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, transport.host+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.GetBody = nil
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Authorization", "Bearer "+transport.token)
	if key != "" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
	}
	return transport.client.Do(request)
}

func readChangeSetResponse(response *http.Response) ([]byte, error) {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	encoding := response.Header.Get("Content-Encoding")
	if err != nil || mediaType != "application/json" || (encoding != "" && encoding != "identity") || response.ContentLength > changeSetResultLimit {
		return nil, newExitError(ExitOperation, "Invalid ChangeSet representation")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, changeSetResultLimit+1))
	if err != nil || len(data) > changeSetResultLimit {
		return nil, newExitError(ExitOperation, "Incomplete or oversized ChangeSet response")
	}
	return data, nil
}

func changeSetVersion(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 || strconv.FormatInt(value, 10) != raw {
		return 0
	}
	return value
}
