package workflows

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPReaderNormalizesWorkflowAndActionEvidence(t *testing.T) {
	const workflowID = "00000000-0000-0000-0000-000000000010"
	const actionID = "00000000-0000-0000-0000-000000000011"
	const planID = "00000000-0000-0000-0000-000000000012"
	const now = "2026-09-17T16:30:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workflow-runs/" + workflowID:
			_, _ = io.WriteString(w, `{"id":"`+workflowID+`","name":"ordered","recipeVersion":"r1","state":"running","currentStep":"copy","deadlineAt":"`+now+`","approvalGates":["exact plan"],"steps":[{"id":"copy","actionPlanId":"`+planID+`","actionRunId":"`+actionID+`","approvalGate":"exact plan","state":"running"}],"aggregateEffectCount":1,"unresolvedCount":0}`)
		case "/api/v1/action-runs/" + actionID:
			_, _ = io.WriteString(w, `{"id":"`+actionID+`","planId":"`+planID+`","attempts":1,"revision":1,"state":"running","outcome":"unknown","effects":["file:1"],"unresolvedEffects":["file:2"],"retryPolicy":{"backoffSeconds":5,"inactivityTimeoutSeconds":30,"maxAttempts":3}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	item, err := reader.GetWorkflow(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("GetWorkflow() error = %v", err)
	}
	if item.ID != workflowID || len(item.Steps) != 1 || item.Steps[0].ActionRunID != actionID || item.Steps[0].Outcome != "unknown" || len(item.Steps[0].Effects) != 2 || item.Steps[0].Effects[1].State != "unresolved" {
		t.Fatalf("normalized workflow = %#v", item)
	}
}

func TestHTTPReaderRejectsUnknownWorkflowField(t *testing.T) {
	const workflowID = "00000000-0000-0000-0000-000000000010"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+workflowID+`","name":"ordered","state":"running","currentStep":null,"deadlineAt":null,"steps":[],"unexpected":true}`)
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.GetWorkflow(context.Background(), workflowID)
	if err == nil || !errors.Is(err, ErrProtocol) {
		t.Fatalf("error = %v", err)
	}
}
