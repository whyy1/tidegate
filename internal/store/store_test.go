package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"tidegate/internal/scenario"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOpenCleansTempAndTrash(t *testing.T) {
	dir := t.TempDir()
	projRoot := filepath.Join(dir, "projects")
	for _, name := range []string{".tmp-abc", ".deleted-xyz"} {
		if err := os.MkdirAll(filepath.Join(projRoot, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListProjects()
	if err != nil || len(list) != 0 {
		t.Fatalf("list=%v err=%v", list, err)
	}
	for _, name := range []string{".tmp-abc", ".deleted-xyz"} {
		if _, err := os.Stat(filepath.Join(projRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("leftover %s not cleaned", name)
		}
	}
}

func TestProjectCRUDAndConcurrency(t *testing.T) {
	s := openTestStore(t)

	if _, err := s.CreateProject(&Project{Name: "   "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty name: got %v, want ErrInvalid", err)
	}
	if _, err := s.CreateProject(&Project{Name: "bad\x00name"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("control char: got %v, want ErrInvalid", err)
	}

	p1, err := s.CreateProject(&Project{
		Name:        "订单服务",
		Description: "  核心接口  ",
		Variables:   KeyValueList{{Name: "api_ver", Value: "v1", Description: "版本"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p1.ID == "" || p1.Version == "" || p1.Description != "核心接口" {
		t.Fatalf("created = %+v", p1)
	}

	p2, err := s.CreateProject(&Project{Name: "用户服务"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListProjects()
	if err != nil || len(list) != 2 || list[0].ID != p1.ID || list[1].ID != p2.ID {
		t.Fatalf("ListProjects = %+v, err=%v", list, err)
	}

	got, err := s.GetProject(p1.ID)
	if err != nil || got.Version != p1.Version || len(got.Variables) != 1 || got.Variables[0].Value != "v1" {
		t.Fatalf("GetProject = %+v, err=%v", got, err)
	}

	// Update with stale or missing version fails.
	if _, err := s.UpdateProject(&Project{ID: p1.ID, Name: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing version: got %v, want ErrInvalid", err)
	}
	if _, err := s.UpdateProject(&Project{ID: p1.ID, Name: "x", Version: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version: got %v, want ErrConflict", err)
	}

	upd, err := s.UpdateProject(&Project{ID: p1.ID, Name: "新订单服务", Description: "v2", Version: p1.Version})
	if err != nil || upd.Name != "新订单服务" || upd.Version == p1.Version {
		t.Fatalf("UpdateProject = %+v, err=%v", upd, err)
	}

	// Delete with stale version fails; delete with current version succeeds.
	if err := s.DeleteProject(p1.ID, p1.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete: got %v, want ErrConflict", err)
	}
	if err := s.DeleteProject(p1.ID, upd.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(p1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: got %v, want ErrNotFound", err)
	}
	if _, err := s.GetProject("../escape"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad ID: got %v, want ErrNotFound", err)
	}
}

func TestKeyValueListYAMLAndJSONCompat(t *testing.T) {
	// Legacy map YAML must decode cleanly into sorted KeyValueList.
	legacyYAML := "b: 2\na: hello\n"
	var kvl KeyValueList
	if err := yaml.Unmarshal([]byte(legacyYAML), &kvl); err != nil {
		t.Fatal(err)
	}
	if len(kvl) != 2 || kvl[0].Name != "a" || kvl[0].Value != "hello" || kvl[1].Name != "b" || kvl[1].Value != "2" {
		t.Fatalf("legacy YAML = %+v", kvl)
	}

	// Array YAML preserves order, duplicates, disabled and description.
	ordered := KeyValueList{
		{Name: "tag", Value: "go"},
		{Name: "tag", Value: "http", Disabled: true, Description: "标签"},
	}
	out, err := yaml.Marshal(ordered)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "- name: tag") {
		t.Fatalf("marshaled YAML:\n%s", out)
	}
	var round KeyValueList
	if err := yaml.Unmarshal(out, &round); err != nil || len(round) != 2 || !round[1].Disabled || round[1].Description != "标签" {
		t.Fatalf("round = %+v err=%v", round, err)
	}

	// Legacy JSON map also works.
	var jList KeyValueList
	if err := json.Unmarshal([]byte(`{"z":true,"a":"1"}`), &jList); err != nil {
		t.Fatal(err)
	}
	if len(jList) != 2 || jList[0].Name != "a" || jList[1].Name != "z" || jList[1].Value != scenario.Scalar("true") {
		t.Fatalf("legacy JSON = %+v", jList)
	}
}
