package workflows

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
			_, _ = io.WriteString(w, `{"id":"`+workflowID+`","name":"ordered","recipeVersion":"r1","state":"running","currentStep":"copy","deadlineAt":"`+now+`","approvalGates":["review"],"steps":[{"id":"copy","actionPlanId":"`+planID+`","actionRunId":"`+actionID+`","approvalGate":"review","state":"running"}],"aggregateEffectCount":1,"unresolvedCount":1}`)
		case "/api/v1/action-plans/" + planID:
			_, _ = io.WriteString(w, `{"id":"`+planID+`","revision":3,"digest":"plan-digest","status":"ready","requiredApproval":"review","action":{"kind":"fs.copy","files":[{"source":{"rootId":"downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"library","relativePath":"Show/E01.mkv"}}]},"manifest":[{"rootId":"downloads","relativePath":"Show/E01.mkv","type":"file","size":42,"fileIdentity":"inode-1","role":"video"}],"preconditions":["source observed"],"capabilities":["copy"],"desiredState":{},"impacts":["library payload is copied"]}`)
		case "/api/v1/action-runs/" + actionID:
			_, _ = io.WriteString(w, `{"id":"`+actionID+`","planId":"`+planID+`","workflowRunId":"`+workflowID+`","stepId":"copy","attempts":1,"revision":1,"state":"running","outcome":"unknown","effects":["file:1"],"unresolvedEffects":["file:2"],"retryPolicy":{"backoffSeconds":5,"inactivityTimeoutSeconds":30,"maxAttempts":3}}`)
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
	if item.ID != workflowID || len(item.Steps) != 1 || item.Steps[0].ActionRunID != actionID || item.Steps[0].Outcome != "unknown" || item.Steps[0].ActionKind != "fs.copy" || item.Steps[0].PlanRevision != 3 || item.Steps[0].PlanDigest != "plan-digest" || len(item.Steps[0].Manifest) != 1 || len(item.Steps[0].Effects) != 1 || len(item.Steps[0].UnresolvedEffects) != 1 || item.Steps[0].UnresolvedEffects[0] != "file:2" {
		t.Fatalf("normalized workflow = %#v", item)
	}
	recorder := httptest.NewRecorder()
	NewHandler(reader).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/"+workflowID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTPReader-to-handler status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{"fs.copy", "plan-digest", "Show/E01.mkv", "file:1", "file:2: unresolved"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("rendered workflow missing %q: %s", expected, recorder.Body.String())
		}
	}
}

func generatedWorkflowPlan(action string) string {
	return `{"id":"00000000-0000-0000-0000-000000000012","revision":4,"digest":"sha256:generated-workflow-fixture","status":"ready","requiredApproval":"review","action":` + action + `,"manifest":[{"rootId":"root-downloads","relativePath":"Show/E01.mkv","type":"file","size":42,"fileIdentity":"inode-1","role":"video"}],"preconditions":["read-back"],"capabilities":["read-only"],"impacts":["unselected payload remains outside this exact plan"],"desiredState":{}}`
}

func TestHTTPReaderGeneratedActionFixturesReachReadOnlyWorkflowHandler(t *testing.T) {
	const workflowID = "00000000-0000-0000-0000-000000000010"
	const planID = "00000000-0000-0000-0000-000000000012"
	actions := map[string]string{
		"arr.registration":  `{"kind":"arr.registration","connectionId":"sonarr-a","mediaKind":"episode","providerId":"tvdb:42","fields":{"monitored":false,"seasonFolder":true,"seriesType":"standard"}}`,
		"arr.import":        `{"kind":"arr.import","connectionId":"sonarr-a","registeredExternalId":"series-42","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"movieOrEpisodeId":42,"subtitle":false}],"previewRevision":"preview-4","transfer":"copy"}`,
		"fs.copy":           `{"kind":"fs.copy","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.hardlink":       `{"kind":"fs.hardlink","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.move":           `{"kind":"fs.move","executor":"mastarr","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.rename":         `{"kind":"fs.rename","executor":"native_client","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"client.stop":       `{"kind":"client.stop","connectionId":"qbittorrent-a","clientItemIds":["torrent-exact-1"]}`,
		"client.remove":     `{"kind":"client.remove","connectionId":"qbittorrent-a","clientItemIds":["torrent-exact-1"],"retainPayload":true}`,
		"fs.trash":          `{"kind":"fs.trash","files":[{"rootId":"root-downloads","relativePath":"Show/E01.mkv"}],"retentionDays":30,"stoppedClientIds":["torrent-exact-1"]}`,
		"fs.restore":        `{"kind":"fs.restore","trashId":"00000000-0000-0000-0000-000000000002","files":[{"source":{"rootId":"root-trash","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.delete":         `{"kind":"fs.delete","files":[{"rootId":"root-trash","relativePath":"Show/E01.mkv"}],"permanent":true,"irreversibleAcknowledgement":true}`,
		"descriptor.delete": `{"kind":"descriptor.delete","descriptorIds":["00000000-0000-0000-0000-000000000003"],"irreversibleAcknowledgement":true}`,
		"jellyfin.refresh":  `{"kind":"jellyfin.refresh","connectionId":"jellyfin-a","scope":"item","itemId":"item-42"}`,
	}
	for kind, action := range actions {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workflow-runs/" + workflowID:
					_, _ = io.WriteString(w, `{"id":"`+workflowID+`","name":"generated-action-fixture","state":"running","currentStep":"fixture","deadlineAt":null,"steps":[{"id":"fixture","actionPlanId":"`+planID+`","approvalGate":"review","state":"queued"}]}`)
				case "/api/v1/action-plans/" + planID:
					_, _ = io.WriteString(w, generatedWorkflowPlan(action))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			item, err := reader.GetWorkflow(context.Background(), workflowID)
			if err != nil {
				t.Fatalf("GetWorkflow() error = %v", err)
			}
			if len(item.Steps) != 1 || item.Steps[0].ActionKind != kind {
				t.Fatalf("normalized step = %#v", item.Steps)
			}
			if kind == "fs.trash" {
				if item.Steps[0].ActionRetention == nil || *item.Steps[0].ActionRetention != 30 || len(item.Steps[0].ActionStoppedIDs) != 1 || item.Steps[0].ActionStoppedIDs[0] != "torrent-exact-1" {
					t.Fatalf("trash authority = %#v", item.Steps[0])
				}
			}
			if kind == "fs.delete" {
				if item.Steps[0].ActionPermanent == nil || !*item.Steps[0].ActionPermanent || !item.Steps[0].ActionIrreversible || len(item.Steps[0].ActionFiles) != 1 || item.Steps[0].ActionFiles[0].SourceRootID != "root-trash" {
					t.Fatalf("delete authority = %#v", item.Steps[0])
				}
			}
			recorder := httptest.NewRecorder()
			NewHandler(reader).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/"+workflowID, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("handler status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			if kind == "fs.trash" {
				for _, expected := range []string{"Retention days", "30", "Stopped-client prerequisites", "torrent-exact-1", "unselected payload remains"} {
					if !strings.Contains(recorder.Body.String(), expected) {
						t.Fatalf("trash body missing %q: %s", expected, recorder.Body.String())
					}
				}
			}
			if kind == "fs.delete" {
				for _, expected := range []string{"Permanent deletion", "true", "unselected payload remains"} {
					if !strings.Contains(recorder.Body.String(), expected) {
						t.Fatalf("delete body missing %q: %s", expected, recorder.Body.String())
					}
				}
			}
		})
	}
}

func TestHTTPReaderRejectsCrossStepEffectIdentityThroughGeneratedRuns(t *testing.T) {
	const workflowID = "00000000-0000-0000-0000-000000000010"
	const planID = "00000000-0000-0000-0000-000000000012"
	const firstRunID = "00000000-0000-0000-0000-000000000013"
	const secondRunID = "00000000-0000-0000-0000-000000000014"
	for _, testCase := range []struct {
		name             string
		aggregate        int
		unresolved       int
		firstEffects     string
		firstUnresolved  string
		secondEffects    string
		secondUnresolved string
	}{
		{name: "duplicate observed", aggregate: 2, firstEffects: `["file:1"]`, secondEffects: `["file:1"]`, firstUnresolved: `[]`, secondUnresolved: `[]`},
		{name: "cross resolved", aggregate: 1, unresolved: 1, firstEffects: `["file:1"]`, secondEffects: `[]`, firstUnresolved: `[]`, secondUnresolved: `["file:1"]`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workflow-runs/" + workflowID:
					_, _ = io.WriteString(w, `{"id":"`+workflowID+`","name":"cross-step-effect-fixture","state":"running","currentStep":"second","aggregateEffectCount":`+strconv.Itoa(testCase.aggregate)+`,"unresolvedCount":`+strconv.Itoa(testCase.unresolved)+`,"deadlineAt":null,"steps":[{"id":"first","actionPlanId":"`+planID+`","actionRunId":"`+firstRunID+`","approvalGate":"review","state":"running"},{"id":"second","actionPlanId":"`+planID+`","actionRunId":"`+secondRunID+`","approvalGate":"review","state":"running"}]}`)
				case "/api/v1/action-plans/" + planID:
					_, _ = io.WriteString(w, generatedWorkflowPlan(`{"kind":"fs.trash","files":[{"rootId":"root-downloads","relativePath":"Show/E01.mkv"}],"retentionDays":30,"stoppedClientIds":["torrent-exact-1"]}`))
				case "/api/v1/action-runs/" + firstRunID:
					_, _ = io.WriteString(w, `{"id":"`+firstRunID+`","planId":"`+planID+`","workflowRunId":"`+workflowID+`","stepId":"first","attempts":1,"revision":1,"state":"running","outcome":"unknown","effects":`+testCase.firstEffects+`,"unresolvedEffects":`+testCase.firstUnresolved+`,"retryPolicy":{"backoffSeconds":5,"inactivityTimeoutSeconds":30,"maxAttempts":3}}`)
				case "/api/v1/action-runs/" + secondRunID:
					_, _ = io.WriteString(w, `{"id":"`+secondRunID+`","planId":"`+planID+`","workflowRunId":"`+workflowID+`","stepId":"second","attempts":1,"revision":1,"state":"running","outcome":"unknown","effects":`+testCase.secondEffects+`,"unresolvedEffects":`+testCase.secondUnresolved+`,"retryPolicy":{"backoffSeconds":5,"inactivityTimeoutSeconds":30,"maxAttempts":3}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			item, err := reader.GetWorkflow(context.Background(), workflowID)
			if err != nil {
				t.Fatalf("GetWorkflow() error = %v", err)
			}
			if len(item.Steps) != 2 || item.Steps[0].ActionRunID != firstRunID || item.Steps[1].ActionRunID != secondRunID {
				t.Fatalf("normalized workflow = %#v", item)
			}
			recorder := httptest.NewRecorder()
			NewHandler(reader).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/"+workflowID, nil))
			if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "incomplete or invalid") {
				t.Fatalf("handler status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
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

func TestHTTPReaderRejectsForeignAndMissingJoinedActionIdentity(t *testing.T) {
	const workflowID = "00000000-0000-0000-0000-000000000010"
	const actionID = "00000000-0000-0000-0000-000000000011"
	const planID = "00000000-0000-0000-0000-000000000012"
	for _, testCase := range []struct {
		name string
		run  string
	}{
		{name: "foreign run", run: `"id":"00000000-0000-0000-0000-000000000021","planId":"00000000-0000-0000-0000-000000000022","workflowRunId":"00000000-0000-0000-0000-000000000023","stepId":"foreign"`},
		{name: "missing run identity", run: `"id":"00000000-0000-0000-0000-000000000000","planId":"` + planID + `","workflowRunId":"` + workflowID + `","stepId":"copy"`},
		{name: "missing plan identity", run: `"id":"` + actionID + `","planId":"00000000-0000-0000-0000-000000000000","workflowRunId":"` + workflowID + `","stepId":"copy"`},
		{name: "missing workflow and step", run: `"id":"` + actionID + `","planId":"` + planID + `"`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workflow-runs/" + workflowID:
					_, _ = io.WriteString(w, `{"id":"`+workflowID+`","name":"ordered","state":"running","currentStep":"copy","deadlineAt":null,"steps":[{"id":"copy","actionPlanId":"`+planID+`","actionRunId":"`+actionID+`","state":"running"}]}`)
				case "/api/v1/action-plans/" + planID:
					_, _ = io.WriteString(w, `{"id":"`+planID+`","revision":1,"digest":"digest","status":"ready","requiredApproval":"review","action":{"kind":"fs.copy","files":[{"source":{"rootId":"downloads","relativePath":"one.mkv"},"destination":{"rootId":"library","relativePath":"one.mkv"}}]},"manifest":[],"preconditions":[],"capabilities":[],"desiredState":{}}`)
				case "/api/v1/action-runs/" + actionID:
					_, _ = io.WriteString(w, `{`+testCase.run+`,"attempts":1,"revision":1,"state":"running","outcome":"unknown","effects":[],"unresolvedEffects":[],"retryPolicy":{"backoffSeconds":1,"inactivityTimeoutSeconds":1,"maxAttempts":1}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			item, err := reader.GetWorkflow(context.Background(), workflowID)
			if err == nil || !errors.Is(err, ErrProtocol) {
				t.Fatalf("GetWorkflow() item=%#v err=%v, want protocol failure", item, err)
			}
		})
	}
}

func TestHTTPReaderRejectsMissingActionPlanIdentity(t *testing.T) {
	const workflowID = "00000000-0000-0000-0000-000000000010"
	const planID = "00000000-0000-0000-0000-000000000012"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workflow-runs/" + workflowID:
			_, _ = io.WriteString(w, `{"id":"`+workflowID+`","name":"ordered","state":"running","currentStep":null,"deadlineAt":null,"steps":[{"id":"copy","actionPlanId":"`+planID+`","state":"queued"}]}`)
		case "/api/v1/action-plans/" + planID:
			_, _ = io.WriteString(w, `{"id":"00000000-0000-0000-0000-000000000000","revision":1,"digest":"digest","status":"ready","requiredApproval":"review","action":{"kind":"fs.copy","files":[{"source":{"rootId":"downloads","relativePath":"one.mkv"},"destination":{"rootId":"library","relativePath":"one.mkv"}}]},"manifest":[],"preconditions":[],"capabilities":[],"desiredState":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetWorkflow(context.Background(), workflowID); err == nil || !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkflow() error = %v, want protocol error", err)
	}
}

func TestHTTPReaderRejectsDuplicateAndUnknownNestedPlanFields(t *testing.T) {
	const workflowID = "00000000-0000-0000-0000-000000000010"
	const planID = "00000000-0000-0000-0000-000000000012"
	workflow := `{"id":"` + workflowID + `","name":"ordered","state":"running","currentStep":null,"deadlineAt":null,"steps":[{"id":"copy","actionPlanId":"` + planID + `","approvalGate":"review","state":"queued"}]}`
	for _, testCase := range []struct {
		name string
		plan string
	}{
		{
			name: "duplicate nested target field",
			plan: `{"id":"` + planID + `","revision":1,"digest":"digest","status":"ready","requiredApproval":"review","action":{"kind":"fs.copy","files":[{"source":{"rootId":"downloads","rootId":"foreign","relativePath":"one.mkv"},"destination":{"rootId":"library","relativePath":"one.mkv"}}]},"manifest":[],"preconditions":[],"capabilities":[],"desiredState":{}}`,
		},
		{
			name: "unknown nested target field",
			plan: `{"id":"` + planID + `","revision":1,"digest":"digest","status":"ready","requiredApproval":"review","action":{"kind":"fs.copy","files":[{"source":{"rootId":"downloads","relativePath":"one.mkv","unexpected":true},"destination":{"rootId":"library","relativePath":"one.mkv"}}]},"manifest":[],"preconditions":[],"capabilities":[],"desiredState":{}}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workflow-runs/" + workflowID:
					_, _ = io.WriteString(w, workflow)
				case "/api/v1/action-plans/" + planID:
					_, _ = io.WriteString(w, testCase.plan)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.GetWorkflow(context.Background(), workflowID); err == nil || !errors.Is(err, ErrProtocol) {
				t.Fatalf("GetWorkflow() error = %v, want protocol error", err)
			}
		})
	}
}
