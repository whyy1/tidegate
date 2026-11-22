package controller

import (
	"fmt"
	"math"
	"strings"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

// ScenarioMixItem configures one weighted scenario participating in a mixed run.
type ScenarioMixItem struct {
	ScenarioID string  `json:"scenario_id"`
	Weight     float64 `json:"weight"`
}

func (c *Controller) resolveMixRun(projectID string, req StartRunRequest) (*store.Resolved, store.Run, error) {
	runMeta := store.Run{
		ProjectID:     projectID,
		EnvironmentID: req.EnvironmentID,
	}
	resolvedItems := make([]*store.Resolved, 0, len(req.Mix))
	names := make([]string, 0, len(req.Mix))
	for i, item := range req.Mix {
		id := strings.TrimSpace(item.ScenarioID)
		if id == "" {
			return nil, runMeta, userErr(ErrInvalidInput, fmt.Sprintf("mix[%d].scenario_id 不能为空", i), nil)
		}
		if math.IsNaN(item.Weight) || math.IsInf(item.Weight, 0) || item.Weight <= 0 {
			return nil, runMeta, userErr(ErrInvalidInput, fmt.Sprintf("mix[%d].weight 必须大于 0", i), nil)
		}
		res, err := c.store.ResolveScenario(projectID, id, store.ResolveOptions{
			EnvironmentID: req.EnvironmentID,
			Variables:     req.Variables,
		})
		if err != nil {
			return nil, runMeta, err
		}
		resolvedItems = append(resolvedItems, res)
		names = append(names, res.Definition.Name)
	}

	firstDef := resolvedItems[0].Definition
	combined := &scenario.Definition{
		Version:   1,
		Name:      buildMixRunName(names),
		Variables: map[string]scenario.Scalar{},
		HTTP:      firstDef.HTTP,
		Load:      firstDef.Load,
	}
	if req.HTTP != nil {
		combined.HTTP = *req.HTTP
	}
	if req.Load != nil {
		combined.Load = *req.Load
	}

	var files []store.ResolvedFile
	seenFiles := map[string]bool{}
	seenData := map[string]scenario.DataSource{}
	branches := make([]scenario.WeightedBranch, 0, len(resolvedItems))

	for idx, res := range resolvedItems {
		def := res.Definition
		for _, rf := range res.Files {
			if !seenFiles[rf.Name] {
				seenFiles[rf.Name] = true
				files = append(files, rf)
			}
		}

		aliasRename := map[string]string{}
		for _, ds := range def.Data {
			if prev, exists := seenData[ds.Name]; exists {
				if prev.File == ds.File && prev.Mode == ds.Mode {
					continue
				}
				newAlias := fmt.Sprintf("mix%d_%s", idx+1, ds.Name)
				for seenData[newAlias].Name != "" {
					newAlias += "_x"
				}
				aliasRename[ds.Name] = newAlias
				ds.Name = newAlias
			}
			seenData[ds.Name] = ds
			combined.Data = append(combined.Data, ds)
		}

		for k, v := range def.Variables {
			if _, exists := combined.Variables[k]; !exists {
				combined.Variables[k] = scenario.Scalar(rewriteDataRefs(string(v), aliasRename))
			}
		}

		stepsCopy := rewriteScenarioSteps(fmt.Sprintf("mix%d_", idx+1), "["+def.Name+"] ", def.Steps, aliasRename)
		branches = append(branches, scenario.WeightedBranch{
			Name:   def.Name,
			Weight: req.Mix[idx].Weight,
			Steps:  stepsCopy,
		})
	}

	combined.Steps = []scenario.Step{{
		ID:       "mix",
		Name:     "多场景流量混合",
		Weighted: &scenario.WeightedStep{Branches: branches},
	}}
	if len(combined.Variables) == 0 {
		combined.Variables = nil
	}
	if files == nil {
		files = []store.ResolvedFile{}
	}
	return &store.Resolved{
		Definition: combined,
		BaseDir:    resolvedItems[0].BaseDir,
		Files:      files,
	}, runMeta, nil
}

func buildMixRunName(names []string) string {
	joined := strings.Join(names, " + ")
	if len(joined) > 120 {
		return "混合压测：" + joined[:117] + "..."
	}
	return "混合压测：" + joined
}

func rewriteDataRefs(s string, renames map[string]string) string {
	if len(renames) == 0 || !strings.Contains(s, "{{") {
		return s
	}
	out := s
	for oldName, newName := range renames {
		out = strings.ReplaceAll(out, "{{"+oldName+".", "{{"+newName+".")
	}
	return out
}

func rewriteScenarioSteps(idPrefix, namePrefix string, steps []scenario.Step, renames map[string]string) []scenario.Step {
	out := make([]scenario.Step, len(steps))
	for i, st := range steps {
		cp := st
		cp.ID = idPrefix + st.ID
		if cp.Name != "" {
			cp.Name = namePrefix + cp.Name
		} else {
			cp.Name = namePrefix + st.ID
		}
		if cp.Request != nil {
			reqCopy := *cp.Request
			reqCopy.URL = rewriteDataRefs(reqCopy.URL, renames)
			if len(reqCopy.Headers) > 0 {
				hdrs := make(map[string]string, len(reqCopy.Headers))
				for k, v := range reqCopy.Headers {
					hdrs[k] = rewriteDataRefs(v, renames)
				}
				reqCopy.Headers = hdrs
			}
			if len(reqCopy.HeaderList) > 0 {
				hl := make([]scenario.NameValue, len(reqCopy.HeaderList))
				for j, h := range reqCopy.HeaderList {
					hl[j] = scenario.NameValue{Name: h.Name, Value: scenario.Scalar(rewriteDataRefs(string(h.Value), renames))}
				}
				reqCopy.HeaderList = hl
			}
			if len(reqCopy.Query) > 0 {
				qm := make(map[string]scenario.Scalar, len(reqCopy.Query))
				for k, v := range reqCopy.Query {
					qm[k] = scenario.Scalar(rewriteDataRefs(string(v), renames))
				}
				reqCopy.Query = qm
			}
			if len(reqCopy.QueryList) > 0 {
				ql := make([]scenario.NameValue, len(reqCopy.QueryList))
				for j, q := range reqCopy.QueryList {
					ql[j] = scenario.NameValue{Name: q.Name, Value: scenario.Scalar(rewriteDataRefs(string(q.Value), renames))}
				}
				reqCopy.QueryList = ql
			}
			if reqCopy.Body != nil {
				bc := *reqCopy.Body
				bc.Content = rewriteDataRefs(bc.Content, renames)
				if len(bc.FieldList) > 0 {
					fl := make([]scenario.NameValue, len(bc.FieldList))
					for j, f := range bc.FieldList {
						fl[j] = scenario.NameValue{Name: f.Name, Value: scenario.Scalar(rewriteDataRefs(string(f.Value), renames))}
					}
					bc.FieldList = fl
				}
				reqCopy.Body = &bc
			}
			if reqCopy.BasicAuth != nil {
				ba := *reqCopy.BasicAuth
				ba.Username = rewriteDataRefs(ba.Username, renames)
				ba.Password = rewriteDataRefs(ba.Password, renames)
				reqCopy.BasicAuth = &ba
			}
			if reqCopy.OAuth2 != nil {
				oa := *reqCopy.OAuth2
				oa.TokenURL = rewriteDataRefs(oa.TokenURL, renames)
				oa.ClientID = rewriteDataRefs(oa.ClientID, renames)
				oa.ClientSecret = rewriteDataRefs(oa.ClientSecret, renames)
				oa.Scope = rewriteDataRefs(oa.Scope, renames)
				reqCopy.OAuth2 = &oa
			}
			cp.Request = &reqCopy
		}
		if cp.Group != nil {
			cp.Group = &scenario.GroupStep{
				Children: rewriteScenarioSteps(idPrefix, namePrefix, cp.Group.Children, renames),
			}
		}
		if cp.Loop != nil {
			cp.Loop = &scenario.LoopStep{
				Count:         cp.Loop.Count,
				While:         rewriteDataRefs(cp.Loop.While, renames),
				MaxIterations: cp.Loop.MaxIterations,
				Children:      rewriteScenarioSteps(idPrefix, namePrefix, cp.Loop.Children, renames),
			}
		}
		if cp.If != nil {
			cp.If = &scenario.IfStep{
				Expr: rewriteDataRefs(cp.If.Expr, renames),
				Then: rewriteScenarioSteps(idPrefix, namePrefix, cp.If.Then, renames),
				Else: rewriteScenarioSteps(idPrefix, namePrefix, cp.If.Else, renames),
			}
		}
		if cp.Weighted != nil {
			brs := make([]scenario.WeightedBranch, len(cp.Weighted.Branches))
			for j, br := range cp.Weighted.Branches {
				brs[j] = scenario.WeightedBranch{
					Name:   br.Name,
					Weight: br.Weight,
					Steps:  rewriteScenarioSteps(idPrefix, namePrefix, br.Steps, renames),
				}
			}
			cp.Weighted = &scenario.WeightedStep{Branches: brs}
		}
		out[i] = cp
	}
	return out
}
