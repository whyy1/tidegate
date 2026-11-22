package api

import (
	"net/http"
	"strconv"
	"strings"

	"tidegate/internal/controller"
	"tidegate/internal/store"
)

// RunDelta is an alias for controller.RunDelta kept for API compatibility.
type RunDelta = controller.RunDelta

// StepDelta is an alias for controller.StepDelta kept for API compatibility.
type StepDelta = controller.StepDelta

// RunComparisonItem is an alias for controller.RunComparisonItem kept for API compatibility.
type RunComparisonItem = controller.RunComparisonItem

func (s *Server) handleCompareRuns(w http.ResponseWriter, r *http.Request) {
	rawIDs := strings.Split(r.URL.Query().Get("ids"), ",")
	seenProj := make(map[string]bool)
	for _, rawID := range rawIDs {
		rid := strings.TrimSpace(rawID)
		if rid == "" {
			continue
		}
		if run, err := s.results.GetRun(r.Context(), rid); err == nil && run.ProjectID != "" && !seenProj[run.ProjectID] {
			seenProj[run.ProjectID] = true
			if _, ok := s.authorizeProject(w, r, run.ProjectID, false); !ok {
				return
			}
		}
	}
	maxPoints := 600
	if rawMax := strings.TrimSpace(r.URL.Query().Get("max_points")); rawMax != "" {
		if n, err := strconv.Atoi(rawMax); err == nil {
			maxPoints = max(1, n)
		}
	}
	rep, err := s.ctrl.CompareRuns(r.Context(), rawIDs, maxPoints)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func downsampleSnapshots(in []store.SnapshotRecord, maxPoints int) []store.SnapshotRecord {
	return controller.DownsampleSnapshots(in, maxPoints)
}
