package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// controllerError carries the controller's status code so the agent can tell
// a transient failure from a refusal that retrying will never fix.
type controllerError struct {
	Path    string
	Status  int
	Message string
}

func (e *controllerError) Error() string {
	return fmt.Sprintf("%s returned %d: %s", e.Path, e.Status, e.Message)
}

// isPermanentAgentError reports whether the controller refused the request in
// a way that retrying cannot resolve: the execution is gone, or this agent is
// not its owner, or the agent is no longer configured.
func isPermanentAgentError(err error) bool {
	var controllerErr *controllerError
	if !errors.As(err, &controllerErr) {
		return false
	}
	switch controllerErr.Status {
	case http.StatusNotFound, http.StatusForbidden, http.StatusUnauthorized:
		return true
	default:
		return false
	}
}

// agentClient performs every controller call over authenticated outbound HTTP.
// The agent never listens on a port.
type agentClient struct {
	baseURL string
	agentID string
	token   string
	client  *http.Client
	poller  *http.Client
	version string
}

func newAgentClient(runtime AgentRuntime, token string) *agentClient {
	return &agentClient{
		baseURL: strings.TrimRight(runtime.ControllerURL, "/"),
		agentID: runtime.ID,
		token:   token,
		client:  &http.Client{Timeout: runtime.RequestTimeout},
		poller:  &http.Client{Timeout: runtime.PollTimeout + 30*time.Second},
		version: currentVersionDetails().Version,
	}
}

func (c *agentClient) call(ctx context.Context, client *http.Client, path string, request, response any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &controllerError{
			Path:    path,
			Status:  resp.StatusCode,
			Message: strings.TrimSpace(string(payload)),
		}
	}
	if response == nil {
		return nil
	}
	return json.Unmarshal(payload, response)
}

func (c *agentClient) Poll(ctx context.Context, current string, known []string) (AgentPollResponse, error) {
	request := AgentPollRequest{
		AgentID:            c.agentID,
		Version:            c.version,
		CurrentExecutionID: current,
		KnownExecutions:    known,
	}
	var response AgentPollResponse
	err := c.call(ctx, c.poller, agentAPIPrefix+"poll", request, &response)
	return response, err
}

func (c *agentClient) Permit(ctx context.Context, executionID string) (AgentPermitResponse, error) {
	var response AgentPermitResponse
	err := c.call(ctx, c.client, agentAPIPrefix+"permit", AgentPermitRequest{AgentID: c.agentID, ExecutionID: executionID}, &response)
	return response, err
}

func (c *agentClient) Heartbeat(ctx context.Context, executionID, state string) (AgentHeartbeatResponse, error) {
	request := AgentHeartbeatRequest{AgentID: c.agentID, Version: c.version, ExecutionID: executionID, State: state}
	var response AgentHeartbeatResponse
	err := c.call(ctx, c.client, agentAPIPrefix+"heartbeat", request, &response)
	return response, err
}

func (c *agentClient) UploadLog(ctx context.Context, executionID string, offset int64, data []byte) (int64, error) {
	request := AgentLogRequest{AgentID: c.agentID, ExecutionID: executionID, Offset: offset, Data: data}
	var response AgentLogResponse
	if err := c.call(ctx, c.client, agentAPIPrefix+"log", request, &response); err != nil {
		return 0, err
	}
	return response.AckOffset, nil
}

func (c *agentClient) SubmitResult(ctx context.Context, request AgentResultRequest) (AgentResultResponse, error) {
	request.AgentID = c.agentID
	var response AgentResultResponse
	err := c.call(ctx, c.client, agentAPIPrefix+"result", request, &response)
	return response, err
}

func (c *agentClient) ReportAttention(ctx context.Context, executionID, message string) error {
	request := AgentAttentionRequest{AgentID: c.agentID, ExecutionID: executionID, Message: message}
	return c.call(ctx, c.client, agentAPIPrefix+"attention", request, nil)
}
