package exporter

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

// ExportArchive streams a complete .tidegate zip package for projectID into w
// (including project metadata, environments, API folders & requests, scenarios,
// and data files), scrubbing any values marked Secret.
func ExportArchive(st *store.Store, projectID string, w io.Writer) error {
	pr, err := st.GetProject(projectID)
	if err != nil {
		return err
	}
	envs, err := st.ListEnvironments(projectID)
	if err != nil {
		return err
	}
	tree, err := st.Tree(projectID)
	if err != nil {
		return err
	}
	scens, err := st.ListScenarios(projectID)
	if err != nil {
		return err
	}
	files, err := st.ListFiles(projectID)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(w)
	writeYAML := func(zipPath string, v any) error {
		b, err := yaml.Marshal(v)
		if err != nil {
			return fmt.Errorf("序列化 %s 失败：%w", zipPath, err)
		}
		fw, err := zw.Create(zipPath)
		if err != nil {
			return err
		}
		_, err = fw.Write(b)
		return err
	}

	prCopy := *pr
	prCopy.Variables = scrubKeyValues(pr.Variables)
	if err := writeYAML("project.yaml", &prCopy); err != nil {
		_ = zw.Close()
		return err
	}

	for _, env := range envs {
		envCopy := *env
		envCopy.Variables = scrubKeyValues(env.Variables)
		if err := writeYAML("environments/"+envCopy.ID+".yaml", &envCopy); err != nil {
			_ = zw.Close()
			return err
		}
	}

	var exportNodes func(dirRel string, nodes []*store.TreeNode) error
	exportNodes = func(dirRel string, nodes []*store.TreeNode) error {
		usedNames := map[string]bool{}
		for _, n := range nodes {
			if n.Type == store.NodeFolder {
				fld, err := st.GetFolder(projectID, n.ID)
				if err != nil {
					return err
				}
				fldCopy := *fld
				fldCopy.Headers = scrubKeyValues(fld.Headers)
				fldCopy.Auth = scrubAuth(fld.Auth)

				seg := uniqueArchiveName(safeSlug(fld.Name), "", usedNames)
				nextDir := path.Join(dirRel, seg)
				if err := writeYAML(path.Join("apis", nextDir, "_folder.yaml"), &fldCopy); err != nil {
					return err
				}
				if err := exportNodes(nextDir, n.Children); err != nil {
					return err
				}
				continue
			}

			a, err := st.GetAPI(projectID, n.ID)
			if err != nil {
				return err
			}
			aCopy := scrubAPI(a)
			fname := uniqueArchiveName(safeSlug(aCopy.Name), ".api.yaml", usedNames)
			if err := writeYAML(path.Join("apis", dirRel, fname), &aCopy); err != nil {
				return err
			}
		}
		return nil
	}
	if err := exportNodes("", tree.Children); err != nil {
		_ = zw.Close()
		return err
	}

	for _, sc := range scens {
		scCopy := scrubScenario(sc)
		if err := writeYAML("scenarios/"+scCopy.ID+".yaml", &scCopy); err != nil {
			_ = zw.Close()
			return err
		}
	}

	for _, df := range files {
		srcPath, err := st.FilePath(projectID, df.Name)
		if err != nil {
			_ = zw.Close()
			return err
		}
		f, err := os.Open(srcPath)
		if err != nil {
			_ = zw.Close()
			return err
		}
		fw, err := zw.Create(path.Join("files", df.Name))
		if err != nil {
			_ = f.Close()
			_ = zw.Close()
			return err
		}
		_, copyErr := io.Copy(fw, f)
		_ = f.Close()
		if copyErr != nil {
			_ = zw.Close()
			return copyErr
		}
	}

	return zw.Close()
}

func scrubKeyValues(in store.KeyValueList) store.KeyValueList {
	if len(in) == 0 {
		return nil
	}
	out := make(store.KeyValueList, len(in))
	for i, kv := range in {
		if kv.Secret {
			kv.Value = ""
		}
		out[i] = kv
	}
	return out
}

func scrubAuth(a *store.Auth) *store.Auth {
	if a == nil {
		return nil
	}
	cp := *a
	if cp.Token != "" && !isTemplateExpr(cp.Token) {
		cp.Token = ""
	}
	if cp.Password != "" && !isTemplateExpr(cp.Password) {
		cp.Password = ""
	}
	if cp.Value != "" && !isTemplateExpr(cp.Value) {
		cp.Value = ""
	}
	if cp.ClientSecret != "" && !isTemplateExpr(cp.ClientSecret) {
		cp.ClientSecret = ""
	}
	return &cp
}

func scrubBody(b *store.APIBody) *store.APIBody {
	if b == nil {
		return nil
	}
	cp := *b
	cp.Fields = scrubKeyValues(b.Fields)
	if len(b.Files) > 0 {
		cp.Files = append([]scenario.BodyFile(nil), b.Files...)
	}
	return &cp
}

func scrubAPI(a *store.API) store.API {
	cp := *a
	cp.Auth = scrubAuth(a.Auth)
	cp.Request.PathParams = scrubKeyValues(a.Request.PathParams)
	cp.Request.Query = scrubKeyValues(a.Request.Query)
	cp.Request.Headers = scrubKeyValues(a.Request.Headers)
	cp.Request.Body = scrubBody(a.Request.Body)
	return cp
}

func scrubScenario(sc *store.Scenario) store.Scenario {
	cp := *sc
	cp.Variables = scrubKeyValues(sc.Variables)
	if len(sc.Steps) > 0 {
		cp.Steps = make([]store.ScenarioStep, len(sc.Steps))
		for i, st := range sc.Steps {
			if st.Override != nil {
				ov := *st.Override
				ov.PathParams = scrubKeyValues(ov.PathParams)
				ov.Headers = scrubKeyValues(ov.Headers)
				ov.Query = scrubKeyValues(ov.Query)
				ov.Body = scrubBody(ov.Body)
				ov.Auth = scrubAuth(ov.Auth)
				st.Override = &ov
			}
			cp.Steps[i] = st
		}
	}
	return cp
}

func isTemplateExpr(s string) bool {
	return strings.Contains(s, "{{") && strings.Contains(s, "}}")
}

func safeSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Trim(b.String(), ". ")
	if s == "" {
		s = "item"
	}
	return s
}

func uniqueArchiveName(stem, ext string, used map[string]bool) string {
	name := stem + ext
	for n := 2; used[strings.ToLower(name)]; n++ {
		name = stem + "-" + strconv.Itoa(n) + ext
	}
	used[strings.ToLower(name)] = true
	return name
}
