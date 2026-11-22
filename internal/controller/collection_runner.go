package controller

import (
	"context"
	"strings"

	"tidegate/internal/engine"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

// CollectionTestRequest executes every API in a project or folder tree in DFS order
// with variable passing and assertions.
type CollectionTestRequest struct {
	FolderID      string                     `json:"folder_id,omitempty"`
	APIIDs        []string                   `json:"api_ids,omitempty"`
	EnvironmentID string                     `json:"environment_id,omitempty"`
	Variables     map[string]scenario.Scalar `json:"variables,omitempty"`
	StopOnError   bool                       `json:"stop_on_error,omitempty"`
	MaxBodyBytes  int64                      `json:"max_body_bytes,omitempty"`
}

// CollectionTestItem summarizes one API execution within a collection test run.
type CollectionTestItem struct {
	StepID       string                 `json:"step_id"`
	APIID        string                 `json:"api_id"`
	APIName      string                 `json:"api_name"`
	FolderPath   string                 `json:"folder_path,omitempty"`
	Protocol     string                 `json:"protocol"`
	Method       string                 `json:"method"`
	Passed       bool                   `json:"passed"`
	Skipped      bool                   `json:"skipped,omitempty"`
	Status       int                    `json:"status,omitempty"`
	DurationMs   float64                `json:"duration_ms"`
	ErrorKind    string                 `json:"error_kind,omitempty"`
	Error        string                 `json:"error,omitempty"`
	AssertTotal  int                    `json:"assert_total"`
	AssertPassed int                    `json:"assert_passed"`
	Step         engine.DebugStepResult `json:"step"`
}

// CollectionTestResult aggregates pass/fail counts and detailed step results for a collection test run.
type CollectionTestResult struct {
	ProjectID     string               `json:"project_id"`
	FolderID      string               `json:"folder_id,omitempty"`
	ScopeName     string               `json:"scope_name"`
	EnvironmentID string               `json:"environment_id,omitempty"`
	Passed        bool                 `json:"passed"`
	Total         int                  `json:"total"`
	PassedCount   int                  `json:"passed_count"`
	FailedCount   int                  `json:"failed_count"`
	SkippedCount  int                  `json:"skipped_count"`
	DurationMs    float64              `json:"duration_ms"`
	Items         []CollectionTestItem `json:"items"`
	FinalVars     map[string]string    `json:"final_vars,omitempty"`
}

// RunCollectionTest walks a project or folder tree, compiles a single-iteration debug
// scenario in DFS order, enforces security policy and redacts secret outputs.
func (c *Controller) RunCollectionTest(ctx context.Context, projectID string, req CollectionTestRequest) (*CollectionTestResult, error) {
	plan, err := c.store.BuildCollectionTestPlan(projectID, strings.TrimSpace(req.FolderID), req.APIIDs)
	if err != nil {
		return nil, err
	}
	envID := strings.TrimSpace(req.EnvironmentID)
	skipThink := true
	debugRes, err := c.DebugScenario(ctx, projectID, DebugScenarioRequest{
		Scenario:      plan.Scenario,
		EnvironmentID: envID,
		Variables:     req.Variables,
		SkipThink:     &skipThink,
		StopOnError:   req.StopOnError,
		MaxBodyBytes:  req.MaxBodyBytes,
		RedactSecrets: true,
	})
	if err != nil {
		return nil, err
	}
	return BuildCollectionTestResult(projectID, envID, plan, debugRes), nil
}

// BuildCollectionTestResult projects a DebugRunResult onto collection API targets.
func BuildCollectionTestResult(projectID, envID string, plan *store.CollectionPlan, debugRes *engine.DebugRunResult) *CollectionTestResult {
	res := &CollectionTestResult{
		ProjectID:     projectID,
		FolderID:      plan.FolderID,
		ScopeName:     plan.ScopeName,
		EnvironmentID: envID,
		Total:         len(plan.Targets),
		Items:         make([]CollectionTestItem, 0, len(plan.Targets)),
	}
	var stepByID map[string]engine.DebugStepResult
	if debugRes != nil {
		res.DurationMs = debugRes.DurationMs
		res.FinalVars = debugRes.FinalVars
		stepByID = make(map[string]engine.DebugStepResult, len(debugRes.Steps))
		for _, st := range debugRes.Steps {
			stepByID[st.StepID] = st
		}
	}

	for _, t := range plan.Targets {
		item := CollectionTestItem{
			StepID:     t.StepID,
			APIID:      t.APIID,
			APIName:    t.APIName,
			FolderPath: t.FolderPath,
			Protocol:   t.Protocol,
			Method:     t.Method,
		}
		st, executed := stepByID[t.StepID]
		if !executed {
			item.Skipped = true
			res.SkippedCount++
			res.Items = append(res.Items, item)
			continue
		}
		item.Step = st
		item.Passed = st.Passed
		item.Status = st.Status
		item.DurationMs = st.Timing.TotalMs
		item.ErrorKind = st.ErrorKind
		item.Error = st.Error
		item.AssertTotal = len(st.Asserts)
		for _, a := range st.Asserts {
			if a.Passed {
				item.AssertPassed++
			}
		}
		if st.Passed {
			res.PassedCount++
		} else {
			res.FailedCount++
		}
		res.Items = append(res.Items, item)
	}
	res.Passed = res.FailedCount == 0 && res.SkippedCount == 0 && res.PassedCount == res.Total
	return res
}
