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
