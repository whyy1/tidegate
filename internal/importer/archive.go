package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"tidegate/internal/store"
)

const (
	maxArchiveEntries      = 10000
	maxArchiveUncompressed = 128 << 20
)

// ImportProjectArchive creates a brand-new project from a .tidegate zip package,
// preserving all environments, folders, APIs, scenarios (remapping step API IDs
// and environment IDs), and uploaded data files.
func ImportProjectArchive(st *store.Store, data []byte, nameOverride string) (*store.Project, *ConversionReport, error) {
	bundle, err := parseArchive(data)
	if err != nil {
		return nil, nil, err
	}
	name := strings.TrimSpace(nameOverride)
	if name == "" {
		name = strings.TrimSpace(bundle.ProjectName)
	}
	if name == "" {
		name = "导入的项目"
	}

	pr, err := st.CreateProject(&store.Project{
		Name:        name,
		Description: bundle.ProjectDesc,
		Variables:   bundle.ProjectVars,
	})
	if err != nil {
		return nil, nil, err
	}

	res, err := Apply(st, pr.ID, PreviewInput{
		Format:   FormatTidegate,
		Content:  data,
		Strategy: StrategyMerge,
	})
	if err != nil {
		_ = st.DeleteProject(pr.ID, "")
		return nil, nil, err
	}
	pr, err = st.GetProject(pr.ID)
	if err != nil {
		return nil, nil, err
	}
	return pr, res.Report, nil
}

func parseArchive(data []byte) (*parsedBundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("打开 .tidegate 压缩包失败：%w", err)
	}
	if len(zr.File) > maxArchiveEntries {
		return nil, fmt.Errorf("压缩包条目数（%d）超过上限 %d", len(zr.File), maxArchiveEntries)
	}

	report := &ConversionReport{
		Format:          FormatTidegate,
		DetectedVersion: "1",
	}
	b := &parsedBundle{Report: report}

	var totalRead int64
	readEntry := func(zf *zip.File) ([]byte, error) {
		if zf.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("压缩包包含不允许的符号链接 %q", zf.Name)
		}
		if err := validateZipMemberPath(zf.Name); err != nil {
			return nil, err
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()

		rem := maxArchiveUncompressed - totalRead
		if rem <= 0 {
			return nil, fmt.Errorf("压缩包解压总大小超过 %d MB 上限", maxArchiveUncompressed>>20)
		}
		raw, err := io.ReadAll(io.LimitReader(rc, rem+1))
		if err != nil {
			return nil, err
		}
		totalRead += int64(len(raw))
		if totalRead > maxArchiveUncompressed {
			return nil, fmt.Errorf("压缩包解压总大小超过 %d MB 上限", maxArchiveUncompressed>>20)
		}
		return raw, nil
	}

	// Collect raw folder metadata keyed by slash-separated directory path under apis/.
	type rawFolderEntry struct {
		dirRel string
		folder store.Folder
	}
	var rawFolders []rawFolderEntry
	type rawAPIEntry struct {
		dirRel string
		api    store.API
	}
	var rawAPIs []rawAPIEntry

	for _, zf := range zr.File {
		if err := validateZipMemberPath(zf.Name); err != nil {
			return nil, err
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(strings.ReplaceAll(zf.Name, "\\", "/"))
		raw, err := readEntry(zf)
		if err != nil {
			return nil, err
		}

		switch {
		case name == "project.yaml":
			var pr store.Project
			if err := yaml.Unmarshal(raw, &pr); err != nil {
				return nil, fmt.Errorf("解析 project.yaml 失败：%w", err)
			}
			b.ProjectName = pr.Name
			b.ProjectDesc = pr.Description
			b.ProjectVars = pr.Variables

		case strings.HasPrefix(name, "environments/") && strings.HasSuffix(name, ".yaml"):
			var env store.Environment
			if err := yaml.Unmarshal(raw, &env); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("跳过损坏的环境文件 %s：%v", name, err))
				continue
			}
			b.Envs = append(b.Envs, parsedEnv{
				OldID: env.ID,
				Env:   env,
			})

		case strings.HasPrefix(name, "apis/") && path.Base(name) == "_folder.yaml":
			dirRel := strings.TrimPrefix(path.Dir(name), "apis")
			dirRel = strings.Trim(dirRel, "/")
			var f store.Folder
			if err := yaml.Unmarshal(raw, &f); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("跳过损坏的文件夹文件 %s：%v", name, err))
				continue
			}
			if dirRel != "" {
				rawFolders = append(rawFolders, rawFolderEntry{dirRel: dirRel, folder: f})
			}

		case strings.HasPrefix(name, "apis/") && strings.HasSuffix(strings.ToLower(name), ".api.yaml"):
			dirRel := strings.TrimPrefix(path.Dir(name), "apis")
			dirRel = strings.Trim(dirRel, "/")
			var a store.API
			if err := yaml.Unmarshal(raw, &a); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("跳过损坏的接口文件 %s：%v", name, err))
				continue
			}
			rawAPIs = append(rawAPIs, rawAPIEntry{dirRel: dirRel, api: a})

		case strings.HasPrefix(name, "scenarios/") && strings.HasSuffix(name, ".yaml"):
			var sc store.Scenario
			if err := yaml.Unmarshal(raw, &sc); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("跳过损坏的场景文件 %s：%v", name, err))
				continue
			}
			b.Scenarios = append(b.Scenarios, parsedScenario{Scenario: sc})

		case strings.HasPrefix(name, "files/") && !strings.Contains(strings.TrimPrefix(name, "files/"), "/"):
			fname := strings.TrimPrefix(name, "files/")
			if fname != "" {
				b.Files = append(b.Files, parsedFile{Name: fname, Content: raw})
			}
		}
	}

	// Sort folders parent-first (shallow depth before deep), then Order.
	sort.Slice(rawFolders, func(i, j int) bool {
		di := strings.Count(rawFolders[i].dirRel, "/")
		dj := strings.Count(rawFolders[j].dirRel, "/")
		if di != dj {
			return di < dj
		}
		if rawFolders[i].folder.Order != rawFolders[j].folder.Order {
			return rawFolders[i].folder.Order < rawFolders[j].folder.Order
		}
		return rawFolders[i].folder.Name < rawFolders[j].folder.Name
	})

	dirNameSegments := map[string][]string{}
	for _, rf := range rawFolders {
		parentDir := path.Dir(rf.dirRel)
		var segs []string
		if parentDir != "." && parentDir != "" {
			if pSegs, ok := dirNameSegments[parentDir]; ok {
				segs = append(segs, pSegs...)
			} else {
				segs = strings.Split(parentDir, "/")
			}
		}
		segs = append(segs, rf.folder.Name)
		dirNameSegments[rf.dirRel] = segs
		b.Folders = append(b.Folders, parsedFolder{
			OldID:   rf.folder.ID,
			Path:    segs,
			Order:   rf.folder.Order,
			Headers: rf.folder.Headers,
			Auth:    rf.folder.Auth,
		})
	}

	sort.Slice(rawAPIs, func(i, j int) bool {
		if rawAPIs[i].dirRel != rawAPIs[j].dirRel {
			return rawAPIs[i].dirRel < rawAPIs[j].dirRel
		}
		if rawAPIs[i].api.Order != rawAPIs[j].api.Order {
			return rawAPIs[i].api.Order < rawAPIs[j].api.Order
		}
		return rawAPIs[i].api.Name < rawAPIs[j].api.Name
	})
	for _, ra := range rawAPIs {
		var fp []string
		if ra.dirRel != "" {
			if segs, ok := dirNameSegments[ra.dirRel]; ok {
				fp = segs
			} else {
				fp = strings.Split(ra.dirRel, "/")
			}
		}
		b.APIs = append(b.APIs, parsedAPI{
			OldID:      ra.api.ID,
			FolderPath: fp,
			API:        ra.api,
		})
	}

	report.APIsFound = len(b.APIs)
	return b, nil
}

func validateZipMemberPath(name string) error {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") {
		return fmt.Errorf("压缩包包含无效路径 %q", name)
	}
	if strings.HasPrefix(name, "/") || (len(name) >= 2 && name[1] == ':') {
		return errors.New("压缩包包含不允许的绝对路径")
	}
	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return fmt.Errorf("压缩包包含越界路径 %q", name)
	}
	return nil
}
