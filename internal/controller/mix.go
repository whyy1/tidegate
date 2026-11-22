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
	secretNames := map[string]bool{}
	branches := make([]scenario.WeightedBranch, 0, len(resolvedItems))

	for idx, res := range resolvedItems {
		for k := range res.SecretNames {
			secretNames[k] = true
		}
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
	if len(secretNames) == 0 {
		secretNames = nil
	}
	return &store.Resolved{
		Definition:  combined,
		BaseDir:     resolvedItems[0].BaseDir,
		Files:       files,
		SecretNames: secretNames,
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

func rewriteStringMap(m map[string]string, renames map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = rewriteDataRefs(v, renames)
	}
	return out
}

func rewriteScalarMap(m map[string]scenario.Scalar, renames map[string]string) map[string]scenario.Scalar {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]scenario.Scalar, len(m))
	for k, v := range m {
		out[k] = scenario.Scalar(rewriteDataRefs(string(v), renames))
	}
	return out
}

func rewriteNVSlice(in []scenario.NameValue, renames map[string]string) []scenario.NameValue {
	if len(in) == 0 {
		return nil
	}
	out := make([]scenario.NameValue, len(in))
	for j, h := range in {
		out[j] = scenario.NameValue{Name: h.Name, Value: scenario.Scalar(rewriteDataRefs(string(h.Value), renames))}
	}
	return out
}

func rewriteBodyCopy(b *scenario.Body, renames map[string]string) *scenario.Body {
	if b == nil {
		return nil
	}
	bc := *b
	bc.Content = rewriteDataRefs(bc.Content, renames)
	bc.FieldList = rewriteNVSlice(bc.FieldList, renames)
	return &bc
}

func rewriteBasicCopy(ba *scenario.BasicAuth, renames map[string]string) *scenario.BasicAuth {
	if ba == nil {
		return nil
	}
	cp := *ba
	cp.Username = rewriteDataRefs(cp.Username, renames)
	cp.Password = rewriteDataRefs(cp.Password, renames)
	return &cp
}

func rewriteOAuthCopy(oa *scenario.OAuth2Auth, renames map[string]string) *scenario.OAuth2Auth {
	if oa == nil {
		return nil
	}
	cp := *oa
	cp.TokenURL = rewriteDataRefs(cp.TokenURL, renames)
	cp.ClientID = rewriteDataRefs(cp.ClientID, renames)
	cp.ClientSecret = rewriteDataRefs(cp.ClientSecret, renames)
	cp.Scope = rewriteDataRefs(cp.Scope, renames)
	return &cp
}

func rewriteScriptDataRefs(code string, renames map[string]string) string {
	if len(renames) == 0 || code == "" {
		return code
	}
	out := code
	for oldName, newName := range renames {
		out = strings.ReplaceAll(out, "data."+oldName+".", "data."+newName+".")
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
			reqCopy.Headers = rewriteStringMap(reqCopy.Headers, renames)
			reqCopy.HeaderList = rewriteNVSlice(reqCopy.HeaderList, renames)
			reqCopy.Query = rewriteScalarMap(reqCopy.Query, renames)
			reqCopy.QueryList = rewriteNVSlice(reqCopy.QueryList, renames)
			reqCopy.Body = rewriteBodyCopy(reqCopy.Body, renames)
			reqCopy.BasicAuth = rewriteBasicCopy(reqCopy.BasicAuth, renames)
			reqCopy.OAuth2 = rewriteOAuthCopy(reqCopy.OAuth2, renames)
			cp.Request = &reqCopy
		}
		if cp.WebSocket != nil {
			wsCopy := *cp.WebSocket
			wsCopy.URL = rewriteDataRefs(wsCopy.URL, renames)
			wsCopy.Headers = rewriteStringMap(wsCopy.Headers, renames)
			wsCopy.HeaderList = rewriteNVSlice(wsCopy.HeaderList, renames)
			if len(wsCopy.Messages) > 0 {
				msgs := make([]scenario.WSMessage, len(wsCopy.Messages))
				for j, m := range wsCopy.Messages {
					msgs[j] = scenario.WSMessage{Type: m.Type, Data: rewriteDataRefs(m.Data, renames)}
				}
				wsCopy.Messages = msgs
			}
			cp.WebSocket = &wsCopy
		}
		if cp.GRPC != nil {
			gCopy := *cp.GRPC
			gCopy.Target = rewriteDataRefs(gCopy.Target, renames)
			gCopy.Metadata = rewriteStringMap(gCopy.Metadata, renames)
			gCopy.MetadataList = rewriteNVSlice(gCopy.MetadataList, renames)
			gCopy.Payload = rewriteDataRefs(gCopy.Payload, renames)
			cp.GRPC = &gCopy
		}
		if cp.SSE != nil {
			seCopy := *cp.SSE
			seCopy.URL = rewriteDataRefs(seCopy.URL, renames)
			seCopy.Headers = rewriteStringMap(seCopy.Headers, renames)
			seCopy.HeaderList = rewriteNVSlice(seCopy.HeaderList, renames)
			seCopy.Query = rewriteScalarMap(seCopy.Query, renames)
			seCopy.QueryList = rewriteNVSlice(seCopy.QueryList, renames)
			seCopy.Body = rewriteBodyCopy(seCopy.Body, renames)
			seCopy.BasicAuth = rewriteBasicCopy(seCopy.BasicAuth, renames)
			seCopy.OAuth2 = rewriteOAuthCopy(seCopy.OAuth2, renames)
			cp.SSE = &seCopy
		}
		if cp.Script != nil {
			scCopy := *cp.Script
			scCopy.Code = rewriteScriptDataRefs(scCopy.Code, renames)
			cp.Script = &scCopy
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
