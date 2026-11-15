package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"

	"github.com/spf13/cobra"
)

const changeSetProposalLimit = 256 * 1024

func newChangeSetRetryCommand(opts *options, client *http.Client) *cobra.Command {
	var workspace, key string
	command := &cobra.Command{
		Use:   "change-set-retry <operation-id>",
		Short: "Prepare a ChangeSet retry proposal for human review from canonical JSON on stdin",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opID, err := operationID(args[0])
			if err != nil {
				return err
			}
			workspaceID, err := operationID(workspace)
			if err != nil {
				return err
			}
			if key == "" {
				return newExitError(ExitArguments, "--idempotency-key is required for the retry proposal")
			}
			if _, err := mutationKey(key); err != nil {
				return err
			}
			body, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), changeSetProposalLimit+1))
			if err != nil || len(body) > changeSetProposalLimit {
				return newExitError(ExitArguments, "ChangeSet proposal input exceeds the 256 KiB JSON limit or could not be read")
			}
			var input struct {
				ID      string            `json:"id"`
				Version string            `json:"expected_version"`
				Mode    string            `json:"mode"`
				Retry   string            `json:"retry_operation_id"`
				Items   []json.RawMessage `json:"operations"`
			}
			if err := json.Unmarshal(body, &input); err != nil {
				return newExitError(ExitArguments, "Provide a canonical ChangeSet retry proposal as JSON on stdin")
			}
			proposalID, idErr := operationID(input.ID)
			version := changeSetVersion(input.Version)
			if idErr != nil || proposalID != input.ID || input.Retry != opID || input.Mode != "per_item" ||
				version == 0 || version == math.MaxInt64 || len(input.Items) == 0 || len(input.Items) > 100 {
				return newExitError(ExitArguments, "Match the original Operation and proposal IDs, exact proposal version and 1–100 selected items in per_item mode")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), operationDeadline)
			defer cancel()
			transport, err := newOperationTransport(ctx, opts, client)
			if err != nil {
				return err
			}
			defer transport.client.CloseIdleConnections()
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Retain --idempotency-key %s with the identical retry proposal JSON and original Workspace %s.\n", key, workspaceID); err != nil {
				return err
			}
			response, err := transport.requestChangeSet(ctx, http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/change-sets", body, key)
			if err != nil {
				return newExitError(ExitOperation, "Retry proposal outcome is uncertain; retain the original key and identical input")
			}
			defer response.Body.Close()
			return printChangeSetRetry(cmd.OutOrStdout(), response, input.ID, version+1)
		},
	}
	command.Flags().StringVar(&workspace, "workspace", "", "Original Workspace UUID (required)")
	command.Flags().StringVar(&key, "idempotency-key", "", "Original retry proposal key (required)")
	return command
}

func printChangeSetRetry(out io.Writer, response *http.Response, id string, version int64) error {
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return newExitError(ExitAuthentication, "Retry preparation requires the original credential family and current access")
	case http.StatusConflict:
		return newExitError(ExitConflict, "Retry proposal could not be reconciled; inspect the original Operation and retain the proposal key")
	case http.StatusBadRequest, http.StatusNotAcceptable, http.StatusUnprocessableEntity:
		return newExitError(ExitArguments, "Retry proposal was rejected (HTTP %d); check the exact source and selected items", response.StatusCode)
	case http.StatusOK:
	default:
		return newExitError(ExitOperation, "Retry preparation returned HTTP %d; retain the original key and input", response.StatusCode)
	}
	data, err := readChangeSetResponse(response)
	if err != nil {
		return err
	}
	var proposal struct {
		ID               string `json:"id"`
		Version          string `json:"version"`
		Digest           string `json:"digest"`
		Mode             string `json:"mode"`
		State            string `json:"state"`
		RequiresApproval bool   `json:"requires_approval"`
	}
	if json.Unmarshal(data, &proposal) != nil || proposal.ID != id || changeSetVersion(proposal.Version) != version ||
		proposal.Digest == "" || proposal.Mode != "per_item" || proposal.State != "review" || !proposal.RequiresApproval {
		return newExitError(ExitOperation, "Unverified retry proposal response; retain the original key and input")
	}
	return json.NewEncoder(out).Encode(json.RawMessage(data))
}
