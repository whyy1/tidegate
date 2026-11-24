package exporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

// ExportScenarioNative exports a stored scenario as a standalone, runnable
// Tidegate scenario definition in YAML or JSON format. Encrypted project or
// environment secrets not explicitly overridden are replaced with a redacted
// placeholder so credentials are never leaked.
func ExportScenarioNative(st *store.Store, projectID, scenarioID string, opts store.ResolveOptions, asYAML bool) ([]byte, error) {
	sc, err := st.GetScenario(projectID, scenarioID)
	if err != nil {
		return nil, err
	}
	envID := strings.TrimSpace(opts.EnvironmentID)
	if envID == "" {
		envID = sc.EnvironmentID
	}

	res, err := st.ResolveScenario(projectID, scenarioID, store.ResolveOptions{
		EnvironmentID:     envID,
		Variables:         opts.Variables,
		SkipSecretDecrypt: true,
	})
	if err != nil {
		return nil, err
	}

	def := *res.Definition
	var secretList []string
	if len(res.SecretNames) > 0 {
		copied := make(map[string]scenario.Scalar, len(def.Variables)+len(res.SecretNames))
		for k, v := range def.Variables {
			copied[k] = v
		}
		for name := range res.SecretNames {
			if _, overridden := opts.Variables[name]; !overridden {
				copied[name] = scenario.Scalar("<REPLACE_SECRET>")
				secretList = append(secretList, name)
			}
		}
		def.Variables = copied
		sort.Strings(secretList)
	}

	if !asYAML {
		raw, err := json.MarshalIndent(&def, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("exporter: marshal scenario json: %w", err)
		}
		return append(raw, '\n'), nil
	}

	var out bytes.Buffer
	out.WriteString("# Tidegate 原生压测场景（可直接运行：tidegate run <scenario>.yaml）\n")
	if len(secretList) > 0 {
		out.WriteString("# 敏感变量已脱敏，运行时可通过 -V key=value 注入，例如：-V ")
		out.WriteString(secretList[0])
		out.WriteString("=...\n")
	}
	out.WriteString("\n")

	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&def); err != nil {
		return nil, fmt.Errorf("exporter: marshal scenario yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("exporter: close yaml encoder: %w", err)
	}
	return out.Bytes(), nil
}
