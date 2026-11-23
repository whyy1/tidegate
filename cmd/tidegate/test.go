package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tidegate/internal/controller"
	"tidegate/internal/engine"
	"tidegate/internal/importer"
	"tidegate/internal/store"
)

type junitTestSuites struct {
	XMLName xml.Name         `xml:"testsuites"`
	Tests   int              `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Skipped int              `xml:"skipped,attr"`
	Time    string           `xml:"time,attr"`
	Suites  []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	Name     string          `xml:"name,attr"`
	Tests    int             `xml:"tests,attr"`
	Failures int             `xml:"failures,attr"`
	Skipped  int             `xml:"skipped,attr"`
	Time     string          `xml:"time,attr"`
	Cases    []junitTestCase `xml:"testcase"`
}

type junitTestCase struct {
	ClassName string        `xml:"classname,attr"`
	Name      string        `xml:"name,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Skipped   *junitSkipped `xml:"skipped,omitempty"`
}

type junitFailure struct {
	Type    string `xml:"type,attr,omitempty"`
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

type junitSkipped struct {
	Message string `xml:"message,attr,omitempty"`
}

func cmdTest(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("test", stderr)
	projectFlag := fs.String("project", envOr("TIDEGATE_PROJECT", ""), "指定项目 ID 或名称（当数据目录包含多个项目时使用）")
	folderFlag := fs.String("folder", "", "仅执行指定目录（支持目录 ID、名称或路径）")
	envFlag := fs.String("env", envOr("TIDEGATE_ENV", ""), "指定环境 ID 或名称")
	stopOnError := fs.Bool("stop-on-error", false, "遇到首个失败接口后立即终止后续接口")
	outPath := fs.String("out", "", "写出完整 JSON 回归测试结果")
	junitPath := fs.String("junit", "", "写出 JUnit XML 测试报告（适用于 CI/CD）")
	quiet := fs.Bool("quiet", false, "仅输出摘要结果")
	timeout := fs.Duration("timeout", 2*time.Minute, "批量测试总超时时间")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return flagError(err)
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "用法：tidegate test <项目目录或.tidegate归档> [--folder 目录] [--env 环境] [--stop-on-error] [--out result.json] [--junit junit.xml]")
		return 2
	}

	st, pid, cleanup, err := openTestProjectStore(pos[0], strings.TrimSpace(*projectFlag))
	if err != nil {
		fmt.Fprintf(stderr, "打开项目失败：%v\n", err)
		return 1
	}
	defer cleanup()

	envID, err := resolveTestEnvironmentID(st, pid, strings.TrimSpace(*envFlag))
	if err != nil {
		fmt.Fprintf(stderr, "解析环境失败：%v\n", err)
		return 1
	}

	plan, err := st.BuildCollectionTestPlan(pid, strings.TrimSpace(*folderFlag), nil)
	if err != nil {
		fmt.Fprintf(stderr, "构建测试计划失败：%v\n", err)
		return 1
	}

	resolved, err := st.ResolveScenarioDraft(pid, plan.Scenario, store.ResolveOptions{EnvironmentID: envID})
	if err != nil {
		fmt.Fprintf(stderr, "解析变量与引用失败：%v\n", err)
		return 1
	}
	compiled, err := resolved.Compile()
	if err != nil {
		fmt.Fprintf(stderr, "编译测试步骤失败：%v\n", err)
		return 1
	}

	runCtx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	debugRes, err := engine.RunDebug(runCtx, compiled, engine.DebugOptions{
		Client:      engine.ClientFast,
		SkipThink:   true,
		StopOnError: *stopOnError,
	})
	if err != nil {
		fmt.Fprintf(stderr, "执行测试失败：%v\n", err)
		return 1
	}

	res := controller.BuildCollectionTestResult(pid, envID, plan, debugRes)
	if *outPath != "" {
		if err := writeJSONReportFile(*outPath, res); err != nil {
			fmt.Fprintf(stderr, "写出 JSON 结果失败：%v\n", err)
			return 1
		}
	}
	if *junitPath != "" {
		if err := writeJUnitXMLFile(*junitPath, res); err != nil {
			fmt.Fprintf(stderr, "写出 JUnit 报告失败：%v\n", err)
			return 1
		}
	}

	if !*quiet {
		for _, item := range res.Items {
			label := item.APIName
			if item.FolderPath != "" {
				label = item.FolderPath + " / " + item.APIName
			}
			switch {
			case item.Skipped:
				fmt.Fprintf(stdout, "[SKIP] %-6s %s\n", item.Method, label)
			case item.Passed:
				fmt.Fprintf(stdout, "[PASS] %-6s %s (HTTP %d, %.1fms)\n", item.Method, label, item.Status, item.DurationMs)
			default:
				fmt.Fprintf(stdout, "[FAIL] %-6s %s (HTTP %d, %.1fms): %s\n", item.Method, label, item.Status, item.DurationMs, item.Error)
			}
		}
	}
	fmt.Fprintf(stdout, "总计: %d | 通过: %d | 失败: %d | 跳过: %d | 耗时: %.1fms\n",
		res.Total, res.PassedCount, res.FailedCount, res.SkippedCount, res.DurationMs)

	if !res.Passed {
		return 3
	}
	return 0
}

func openTestProjectStore(targetPath, projectRef string) (*store.Store, string, func(), error) {
	noop := func() {}
	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return nil, "", noop, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, "", noop, err
	}

	if !info.IsDir() {
		raw, err := os.ReadFile(absPath)
		if err != nil {
			return nil, "", noop, err
		}
		tmpDir, err := os.MkdirTemp("", "tidegate-test-*")
		if err != nil {
			return nil, "", noop, err
		}
		cleanup := func() { _ = os.RemoveAll(tmpDir) }
		st, err := store.Open(tmpDir)
		if err != nil {
			cleanup()
			return nil, "", noop, err
		}
		pr, _, err := importer.ImportProjectArchive(st, raw, "")
		if err != nil {
			cleanup()
			return nil, "", noop, err
		}
		return st, pr.ID, cleanup, nil
	}

	// Single project directory containing project.yaml directly.
	if _, err := os.Stat(filepath.Join(absPath, "project.yaml")); err == nil {
		tmpDir, err := os.MkdirTemp("", "tidegate-test-*")
		if err != nil {
			return nil, "", noop, err
		}
		cleanup := func() { _ = os.RemoveAll(tmpDir) }
		projDest := filepath.Join(tmpDir, "projects", "local")
		if err := os.MkdirAll(projDest, 0o755); err != nil {
			cleanup()
			return nil, "", noop, err
		}
		if err := os.CopyFS(projDest, os.DirFS(absPath)); err != nil {
			cleanup()
			return nil, "", noop, err
		}
		st, err := store.Open(tmpDir)
		if err != nil {
			cleanup()
			return nil, "", noop, err
		}
		return st, "local", cleanup, nil
	}

	st, err := store.Open(absPath)
	if err != nil {
		return nil, "", noop, err
	}
	projects, err := st.ListProjects()
	if err != nil {
		return nil, "", noop, err
	}
	if len(projects) == 0 {
		return nil, "", noop, fmt.Errorf("目录 %s 下未找到 Tidegate 项目", absPath)
	}
	if projectRef == "" {
		if len(projects) > 1 {
			return nil, "", noop, fmt.Errorf("目录 %s 包含 %d 个项目，请通过 --project 指定项目 ID 或名称", absPath, len(projects))
		}
		return st, projects[0].ID, noop, nil
	}
	for _, p := range projects {
		if p.ID == projectRef || strings.EqualFold(p.Name, projectRef) {
			return st, p.ID, noop, nil
		}
	}
	return nil, "", noop, fmt.Errorf("未找到匹配 --project %q 的项目", projectRef)
}

func resolveTestEnvironmentID(st *store.Store, projectID, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	envs, err := st.ListEnvironments(projectID)
	if err != nil {
		return "", err
	}
	for _, e := range envs {
		if e.ID == ref || strings.EqualFold(e.Name, ref) {
			return e.ID, nil
		}
	}
	return "", fmt.Errorf("环境 %q 不存在", ref)
}

func writeJSONReportFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func writeJUnitXMLFile(path string, res *controller.CollectionTestResult) error {
	cases := make([]junitTestCase, 0, len(res.Items))
	for _, item := range res.Items {
		cls := item.FolderPath
		if cls == "" {
			cls = res.ScopeName
		}
		tc := junitTestCase{
			ClassName: cls,
			Name:      fmt.Sprintf("%s %s", item.Method, item.APIName),
			Time:      fmt.Sprintf("%.6f", item.DurationMs/1000.0),
		}
		if item.Skipped {
			tc.Skipped = &junitSkipped{Message: "由于前置步骤失败且启用 stop-on-error 而跳过"}
		} else if !item.Passed {
			msg := item.Error
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", item.Status)
			}
			var detail strings.Builder
			detail.WriteString(msg)
			for _, a := range item.Step.Asserts {
				if !a.Passed {
					fmt.Fprintf(&detail, "\n[ASSERT FAIL] %s: %s", a.Desc, a.Message)
				}
			}
			tc.Failure = &junitFailure{
				Type:    item.ErrorKind,
				Message: msg,
				Body:    detail.String(),
			}
		}
		cases = append(cases, tc)
	}
	totalSec := fmt.Sprintf("%.6f", res.DurationMs/1000.0)
	doc := junitTestSuites{
		Tests:    res.Total,
		Failures: res.FailedCount,
		Skipped:  res.SkippedCount,
		Time:     totalSec,
		Suites: []junitTestSuite{{
			Name:     res.ScopeName,
			Tests:    res.Total,
			Failures: res.FailedCount,
			Skipped:  res.SkippedCount,
			Time:     totalSec,
			Cases:    cases,
		}},
	}
	b, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	payload := append([]byte(xml.Header), b...)
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}
