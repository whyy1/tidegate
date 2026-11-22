package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tidegate/internal/scenario"
)

func TestFolderAPITreeCopyMoveAndDraftSave(t *testing.T) {
	s := openTestStore(t)
	pr, err := s.CreateProject(&Project{Name: "测试项目"})
	if err != nil {
		t.Fatal(err)
	}

	// Create nested folders and duplicate-named APIs.
	f1, err := s.CreateFolder(pr.ID, &Folder{
		Name:    "用户模块",
		Headers: KeyValueList{{Name: "X-Module", Value: "user"}},
		Auth:    &Auth{Type: AuthBearer}, // incomplete draft auth allowed on save
	})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := s.CreateFolder(pr.ID, &Folder{Name: "子目录", ParentID: f1.ID})
	if err != nil {
		t.Fatal(err)
	}

	a1, err := s.CreateAPI(pr.ID, &API{
		Name:     "登录",
		FolderID: f1.ID,
		Request: APIRequest{
			Method: "post",
			URL:    "/login",
			Body: &APIBody{
				Type:    "none",
				Content: `{"draft":"kept"}`, // kept even when Type is none
			},
			Headers: KeyValueList{{Name: "", Value: "", Disabled: true}}, // empty disabled draft row allowed
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := s.CreateAPI(pr.ID, &API{
		Name:     "登录", // duplicate name in same folder
		FolderID: f1.ID,
		Request:  APIRequest{Method: "GET", URL: "/login2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify duplicate filenames coexist under apis/用户模块.
	des, err := os.ReadDir(filepath.Join(s.Root(), pr.ID, apisDir, "用户模块"))
	if err != nil {
		t.Fatal(err)
	}
	var apiFiles []string
	for _, de := range des {
		if strings.HasSuffix(de.Name(), apiExt) {
			apiFiles = append(apiFiles, de.Name())
		}
	}
	if len(apiFiles) != 2 {
		t.Fatalf("expected 2 api files, got %v", apiFiles)
	}

	// Copy a1.
	copied, err := s.CopyAPI(pr.ID, a1.ID)
	if err != nil || copied.ID == a1.ID || !strings.HasSuffix(copied.Name, copySuffix) {
		t.Fatalf("CopyAPI = %+v err=%v", copied, err)
	}

	// Rename a2 -> file on disk should rename accordingly.
	a2Upd, err := s.UpdateAPI(pr.ID, &API{
		ID:      a2.ID,
		Name:    "登出",
		Version: a2.Version,
		Request: a2.Request,
	})
	if err != nil || a2Upd.Request.Method != "GET" {
		t.Fatalf("UpdateAPI = %+v err=%v", a2Upd, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), pr.ID, apisDir, "用户模块", "登出.api.yaml")); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	// Cannot move folder f1 into its own child f2.
	if _, err := s.Move(pr.ID, MoveRequest{ID: f1.ID, Version: f1.Version, ParentID: f2.ID, Index: 0}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle move: got %v, want ErrInvalid", err)
	}

	// Move copied API to top level at index 0.
	mv, err := s.Move(pr.ID, MoveRequest{ID: copied.ID, Version: copied.Version, ParentID: "", Index: 0})
	if err != nil || mv.ParentID != "" {
		t.Fatalf("Move = %+v err=%v", mv, err)
	}

	tree, err := s.Tree(pr.ID)
	if err != nil || len(tree.Children) != 2 {
		t.Fatalf("Tree = %+v err=%v", tree, err)
	}
	if tree.Children[0].ID != copied.ID || tree.Children[1].ID != f1.ID {
		t.Fatalf("unexpected top-level order: %+v, %+v", tree.Children[0].ID, tree.Children[1].ID)
	}

	// Delete a2 and f1.
	if err := s.DeleteAPI(pr.ID, a2Upd.ID, a2Upd.Version); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFolder(pr.ID, f1.ID, f1.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAPI(pr.ID, a1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("child API after folder delete: got %v", err)
	}
}

func TestFilesUploadPreviewAndValidation(t *testing.T) {
	s := openTestStore(t)
	pr, err := s.CreateProject(&Project{Name: "文件测试"})
	if err != nil {
		t.Fatal(err)
	}

	// Reject path traversal and Windows reserved names.
	for _, bad := range []string{"../x.csv", "a/b.csv", ".hidden", "CON.csv", " space.csv"} {
		if _, err := s.SaveFile(pr.ID, bad, strings.NewReader("a\n1\n"), ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad filename %q: got %v, want ErrInvalid", bad, err)
		}
	}

	csvBody := "\xef\xbb\xbfname,pwd\nalice,123\nbob,456\ncharlie,789\n"
	df, err := s.SaveFile(pr.ID, "users.csv", strings.NewReader(csvBody), "")
	if err != nil || df.Name != "users.csv" || df.Size == 0 {
		t.Fatalf("SaveFile = %+v err=%v", df, err)
	}
	// Saving again without version triggers conflict.
	if _, err := s.SaveFile(pr.ID, "users.csv", strings.NewReader(csvBody), ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate SaveFile: got %v, want ErrConflict", err)
	}

	pv, err := s.PreviewFile(pr.ID, "users.csv", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.IsCSV || pv.TotalRows != 3 || len(pv.Rows) != 2 || len(pv.Columns) != 2 || pv.Columns[0] != "name" {
		t.Fatalf("PreviewFile = %+v", pv)
	}

	// Non-CSV text file preview.
	if _, err := s.SaveFile(pr.ID, "payload.json", strings.NewReader(`{"k":"v"}`), ""); err != nil {
		t.Fatal(err)
	}
	jpv, err := s.PreviewFile(pr.ID, "payload.json", 10)
	if err != nil || jpv.IsCSV || jpv.Text != `{"k":"v"}` {
		t.Fatalf("json PreviewFile = %+v err=%v", jpv, err)
	}

	list, err := s.ListFiles(pr.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListFiles = %+v err=%v", list, err)
	}
	if err := s.DeleteFile(pr.ID, "users.csv", df.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StatFile(pr.ID, "users.csv"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StatFile after delete: got %v", err)
	}

	// Scenario CRUD basics.
	sc, err := s.CreateScenario(pr.ID, &Scenario{
		Name: "空壳场景",
		Load: scenario.Load{Stages: []scenario.Stage{{Duration: 1e9, Target: 1}}},
	})
	if err != nil || sc.ID == "" {
		t.Fatalf("CreateScenario = %+v err=%v", sc, err)
	}
	if err := s.DeleteScenario(pr.ID, sc.ID, sc.Version); err != nil {
		t.Fatal(err)
	}
}
