package store

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	apisDir    = "apis"
	folderFile = "_folder.yaml"
	apiExt     = ".api.yaml"
	// orderStep is the gap between the orders of consecutive siblings, so
	// that most moves only rewrite the moved item.
	orderStep = 1024
	maxDepth  = 32
)

// Node types of TreeNode.Type and MoveResult.Type.
const (
	NodeFolder = "folder"
	NodeAPI    = "api"
)

type entryKind uint8

const (
	entryFolder entryKind = iota + 1
	entryAPI
)

func (k entryKind) kind() Kind {
	if k == entryFolder {
		return KindFolder
	}
	return KindAPI
}

// entry locates a folder or API.
type entry struct {
	kind entryKind
	// rel is the slash separated path relative to the apis directory: the
	// directory of a folder or the file of an API.
	rel string
	// parent is the ID of the containing folder, empty at the top level.
	parent string
}

// TreeNode is a folder or API in the tree listing.
type TreeNode struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Order   int64  `json:"order"`
	Version string `json:"version"`
	// Method and URL are set for APIs.
	Method string `json:"method,omitempty"`
	URL    string `json:"url,omitempty"`
	// Headers and Auth are set for folders.
	Headers  KeyValueList `json:"headers,omitempty"`
	Auth     *Auth        `json:"auth,omitempty"`
	Children []*TreeNode  `json:"children,omitempty"`
}

// Problem is a file that could not be loaded while listing the tree.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Tree is the folder and API tree of a project. Children of every level are
// sorted by order, then name.
type Tree struct {
	Children []*TreeNode `json:"children"`
	Problems []Problem   `json:"problems,omitempty"`
}

// MoveRequest moves a folder or API to a position in a folder.
type MoveRequest struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	// ParentID is the target folder, empty for the top level.
	ParentID string `json:"parent_id"`
	// Index is the position among the target folder's children (folders
	// and APIs together, as listed by Tree) after the move. A negative
	// index or one past the end appends.
	Index int `json:"index"`
}

// MoveResult describes a moved item.
type MoveResult struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Order    int64  `json:"order"`
	Version  string `json:"version"`
}

func (p *project) abs(rel string) string {
	return filepath.Join(p.dir, apisDir, filepath.FromSlash(rel))
}

// dirOf returns the slash separated parent directory of rel, "" for the
// apis directory itself.
func dirOf(rel string) string {
	d := path.Dir(rel)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

func isAPIFile(name string) bool {
	return len(name) > len(apiExt) && strings.HasSuffix(strings.ToLower(name), apiExt)
}

func (p *project) readFolderAt(rel string) (*Folder, error) {
	var f Folder
	v, err := readYAML(filepath.Join(p.abs(rel), folderFile), &f)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("缺少 %s，已忽略这个目录", folderFile)
		}
		return nil, err
	}
	id, ok := normalizeID(f.ID)
	if !ok {
		return nil, fmt.Errorf("%s 中的 id %q 无效", folderFile, f.ID)
	}
	f.ID, f.Version = id, v
	return &f, nil
}

func (p *project) readAPIAt(rel string) (*API, error) {
	var a API
	v, err := readYAML(p.abs(rel), &a)
	if err != nil {
		return nil, err
	}
	id, ok := normalizeID(a.ID)
	if !ok {
		return nil, fmt.Errorf("id %q 无效", a.ID)
	}
	a.ID, a.Version = id, v
	return &a, nil
}

func (p *project) writeFolder(rel string, f *Folder) error {
	v, err := writeYAML(filepath.Join(p.abs(rel), folderFile), f)
	if err == nil {
		f.Version = v
	}
	return err
}

func (p *project) writeAPI(rel string, a *API) error {
	v, err := writeYAML(p.abs(rel), a)
	if err == nil {
		a.Version = v
	}
	return err
}

func folderNode(f *Folder) *TreeNode {
	return &TreeNode{Type: NodeFolder, ID: f.ID, Name: f.Name, Order: f.Order, Version: f.Version, Headers: f.Headers, Auth: f.Auth}
}

func apiNode(a *API) *TreeNode {
	m := a.Request.Method
	if m == "" {
		m = "GET"
	}
	return &TreeNode{Type: NodeAPI, ID: a.ID, Name: a.Name, Order: a.Order, Version: a.Version, Method: m, URL: a.Request.URL}
}

func lessByOrder(oi, oj int64, ni, nj, idi, idj string) bool {
	if oi != oj {
		return oi < oj
	}
	if li, lj := strings.ToLower(ni), strings.ToLower(nj); li != lj {
		return li < lj
	}
	return idi < idj
}

// scan reads the whole tree and rebuilds the index.
func (p *project) scan() *Tree {
	t := &Tree{}
	idx := map[string]entry{}
	t.Children = p.scanDir("", "", 0, idx, t)
	if t.Children == nil {
		t.Children = []*TreeNode{}
	}
	p.idx = idx
	return t
}

func (t *Tree) problem(rel string, msg string) {
	t.Problems = append(t.Problems, Problem{Path: path.Join(apisDir, rel), Message: msg})
}

func (p *project) scanDir(rel, parentID string, depth int, idx map[string]entry, t *Tree) []*TreeNode {
	des, err := os.ReadDir(p.abs(rel))
	if err != nil {
		if rel != "" || !errors.Is(err, os.ErrNotExist) {
			t.problem(rel, "读取目录失败："+err.Error())
		}
		return nil
	}
	var out []*TreeNode
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		crel := path.Join(rel, name)
		switch {
		case de.IsDir():
			if depth >= maxDepth {
				t.problem(crel, "文件夹层级过深，已忽略")
				continue
			}
			f, err := p.readFolderAt(crel)
			if err != nil {
				t.problem(crel, err.Error())
				continue
			}
			if _, dup := idx[f.ID]; dup {
				t.problem(crel, fmt.Sprintf("ID %s 和其他文件夹或接口重复，已忽略", f.ID))
				continue
			}
			idx[f.ID] = entry{kind: entryFolder, rel: crel, parent: parentID}
			n := folderNode(f)
			n.Children = p.scanDir(crel, f.ID, depth+1, idx, t)
			out = append(out, n)
		case de.Type().IsRegular() && isAPIFile(name):
			a, err := p.readAPIAt(crel)
			if err != nil {
				t.problem(crel, err.Error())
				continue
			}
			if _, dup := idx[a.ID]; dup {
				t.problem(crel, fmt.Sprintf("ID %s 和其他文件夹或接口重复，已忽略", a.ID))
				continue
			}
			idx[a.ID] = entry{kind: entryAPI, rel: crel, parent: parentID}
			out = append(out, apiNode(a))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return lessByOrder(out[i].Order, out[j].Order, out[i].Name, out[j].Name, out[i].ID, out[j].ID)
	})
	return out
}

// loadFolder finds a folder by ID, rescanning once if the index is stale.
func (p *project) loadFolder(id string) (*Folder, entry, error) {
	nid, ok := normalizeID(id)
	if !ok {
		return nil, entry{}, notFound(KindFolder, id)
	}
	fresh := p.idx == nil
	if fresh {
		p.scan()
	}
	for {
		if e, ok := p.idx[nid]; ok && e.kind == entryFolder {
			if f, err := p.readFolderAt(e.rel); err == nil && f.ID == nid {
				f.ParentID = e.parent
				return f, e, nil
			}
		}
		if fresh {
			return nil, entry{}, notFound(KindFolder, id)
		}
		p.scan()
		fresh = true
	}
}

// loadAPI finds an API by ID, rescanning once if the index is stale.
func (p *project) loadAPI(id string) (*API, entry, error) {
	nid, ok := normalizeID(id)
	if !ok {
		return nil, entry{}, notFound(KindAPI, id)
	}
	fresh := p.idx == nil
	if fresh {
		p.scan()
	}
	for {
		if e, ok := p.idx[nid]; ok && e.kind == entryAPI {
			if a, err := p.readAPIAt(e.rel); err == nil && a.ID == nid {
				a.FolderID = e.parent
				return a, e, nil
			}
		}
		if fresh {
			return nil, entry{}, notFound(KindAPI, id)
		}
		p.scan()
		fresh = true
	}
}

// folderDir returns the directory of a folder ID ("" is the top level) and
// the canonical folder ID.
func (p *project) folderDir(id string) (rel, folderID string, err error) {
	if id == "" {
		if err := os.MkdirAll(p.abs(""), dirPerm); err != nil {
			return "", "", fmt.Errorf("创建目录失败：%w", err)
		}
		return "", "", nil
	}
	f, e, err := p.loadFolder(id)
	if err != nil {
		return "", "", err
	}
	return e.rel, f.ID, nil
}

// folderChain returns the folders from the top level down to the folder at
// rel.
func (p *project) folderChain(rel string) ([]*Folder, error) {
	if rel == "" {
		return nil, nil
	}
	parts := strings.Split(rel, "/")
	out := make([]*Folder, 0, len(parts))
	for i := range parts {
		f, err := p.readFolderAt(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func (p *project) setIndex(id string, e entry) {
	if p.idx != nil {
		p.idx[id] = e
	}
}

// sibling is a child of a directory, used for ordering.
type sibling struct {
	id    string
	kind  entryKind
	name  string
	order int64
	rel   string
}

// takenNames returns the lower case names in a directory, plus the names
// reserved for folder metadata.
func (p *project) takenNames(rel string) (map[string]bool, error) {
	des, err := os.ReadDir(p.abs(rel))
	if err != nil {
		return nil, fmt.Errorf("读取目录失败：%w", err)
	}
	taken := map[string]bool{folderFile: true}
	for _, de := range des {
		taken[strings.ToLower(de.Name())] = true
	}
	return taken, nil
}

// children returns the folders and APIs directly in a directory, sorted.
func (p *project) children(rel string) ([]sibling, error) {
	des, err := os.ReadDir(p.abs(rel))
	if err != nil {
		return nil, fmt.Errorf("读取目录失败：%w", err)
	}
	var sibs []sibling
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		crel := path.Join(rel, name)
		if de.IsDir() {
			if f, err := p.readFolderAt(crel); err == nil {
				sibs = append(sibs, sibling{id: f.ID, kind: entryFolder, name: f.Name, order: f.Order, rel: crel})
			}
		} else if de.Type().IsRegular() && isAPIFile(name) {
			if a, err := p.readAPIAt(crel); err == nil {
				sibs = append(sibs, sibling{id: a.ID, kind: entryAPI, name: a.Name, order: a.Order, rel: crel})
			}
		}
	}
	sort.Slice(sibs, func(i, j int) bool {
		return lessByOrder(sibs[i].order, sibs[j].order, sibs[i].name, sibs[j].name, sibs[i].id, sibs[j].id)
	})
	return sibs, nil
}

// orderAt returns the order that places an item at position i of sibs
// (which must not contain the item). ok is false when there is no gap and
// the siblings must be renumbered.
func orderAt(sibs []sibling, i int) (order int64, ok bool) {
	const limit = math.MaxInt64 / 4
	n := len(sibs)
	switch {
	case n == 0:
		return orderStep, true
	case i == 0:
		if sibs[0].order < -limit {
			return 0, false
		}
		return sibs[0].order - orderStep, true
	case i >= n:
		if sibs[n-1].order > limit {
			return 0, false
		}
		return sibs[n-1].order + orderStep, true
	}
	lo, hi := sibs[i-1].order, sibs[i].order
	if hi-lo >= 2 {
		return lo + (hi-lo)/2, true
	}
	return 0, false
}

// placeAt returns the order for an item inserted at position i of sibs,
// renumbering the siblings when there is no room.
func (p *project) placeAt(sibs []sibling, i int) (int64, error) {
	if i < 0 || i > len(sibs) {
		i = len(sibs)
	}
	if order, ok := orderAt(sibs, i); ok {
		return order, nil
	}
	var itemOrder int64
	pos := int64(1)
	for k := 0; k <= len(sibs); k++ {
		if k == i {
			itemOrder = pos * orderStep
			pos++
		}
		if k == len(sibs) {
			break
		}
		if want := pos * orderStep; sibs[k].order != want {
			if err := p.setOrder(sibs[k], want); err != nil {
				return 0, err
			}
		}
		pos++
	}
	return itemOrder, nil
}

func (p *project) setOrder(s sibling, order int64) error {
	if s.kind == entryFolder {
		f, err := p.readFolderAt(s.rel)
		if err != nil {
			return err
		}
		f.Order = order
		return p.writeFolder(s.rel, f)
	}
	a, err := p.readAPIAt(s.rel)
	if err != nil {
		return err
	}
	a.Order = order
	return p.writeAPI(s.rel, a)
}

// renameFor moves the file or directory at rel to a name derived from
// newName in directory dir when the derived name differs from the one
// derived from oldName, and returns the new rel path.
func (p *project) renameFor(rel, dir, oldName, newName, ext string, force bool) (string, error) {
	stem := fileStem(newName)
	if !force && stem == fileStem(oldName) {
		return rel, nil
	}
	taken, err := p.takenNames(dir)
	if err != nil {
		return "", err
	}
	if dirOf(rel) == dir {
		delete(taken, strings.ToLower(path.Base(rel)))
	}
	newRel := path.Join(dir, uniqueName(stem, ext, taken))
	if newRel == rel {
		return rel, nil
	}
	if err := renameRetry(p.abs(rel), p.abs(newRel)); err != nil {
		return "", fmt.Errorf("重命名失败：%w", err)
	}
	return newRel, nil
}

// Tree lists the folders and APIs of a project. Files that cannot be read
// are reported in Problems instead of failing the listing.
func (s *Store) Tree(projectID string) (*Tree, error) {
	var t *Tree
	err := s.with(projectID, func(p *project) error {
		t = p.scan()
		return nil
	})
	return t, err
}

// GetFolder returns one folder.
func (s *Store) GetFolder(projectID, id string) (*Folder, error) {
	var out *Folder
	err := s.with(projectID, func(p *project) (err error) {
		out, _, err = p.loadFolder(id)
		return err
	})
	return out, err
}

// CreateFolder creates a folder at the end of in.ParentID (empty for the
// top level). Name, Headers and Auth of in are used.
func (s *Store) CreateFolder(projectID string, in *Folder) (*Folder, error) {
	var out *Folder
	err := s.with(projectID, func(p *project) error {
		f := Folder{Name: in.Name, Headers: in.Headers, Auth: in.Auth}
		if err := f.normalize(); err != nil {
			return err
		}
		parentRel, parentID, err := p.folderDir(in.ParentID)
		if err != nil {
			return withParentField(err)
		}
		sibs, err := p.children(parentRel)
		if err != nil {
			return err
		}
		taken, err := p.takenNames(parentRel)
		if err != nil {
			return err
		}
		if f.Order, err = p.placeAt(sibs, -1); err != nil {
			return err
		}
		f.ID, f.ParentID = newID(), parentID
		rel := path.Join(parentRel, uniqueName(fileStem(f.Name), "", taken))
		// Build under a hidden name so a half created folder is never seen.
		tmp := filepath.Join(p.abs(parentRel), tempPrefix+randomSuffix())
		if err := os.Mkdir(tmp, dirPerm); err != nil {
			return fmt.Errorf("创建文件夹失败：%w", err)
		}
		v, err := writeYAML(filepath.Join(tmp, folderFile), &f)
		if err == nil {
			err = renameRetry(tmp, p.abs(rel))
		}
		if err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("创建文件夹失败：%w", err)
		}
		f.Version = v
		p.setIndex(f.ID, entry{kind: entryFolder, rel: rel, parent: parentID})
		out = &f
		return nil
	})
	return out, err
}

// withParentField reports a missing parent folder as a validation error of
// the parent_id field instead of a missing resource.
func withParentField(err error) error {
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return invalid("parent_id", "目标文件夹 %q 不存在", nf.ID)
	}
	return err
}

// UpdateFolder changes the name, headers and auth of in.ID. Renaming
// renames the directory. in.Version must be the version the client read.
func (s *Store) UpdateFolder(projectID string, in *Folder) (*Folder, error) {
	var out *Folder
	err := s.with(projectID, func(p *project) error {
		cur, e, err := p.loadFolder(in.ID)
		if err != nil {
			return err
		}
		if err := checkVersion(KindFolder, cur.ID, in.Version, cur.Version); err != nil {
			return err
		}
		f := Folder{ID: cur.ID, Name: in.Name, Order: cur.Order, Headers: in.Headers, Auth: in.Auth, ParentID: cur.ParentID}
		if err := f.normalize(); err != nil {
			return err
		}
		rel, err := p.renameFor(e.rel, dirOf(e.rel), cur.Name, f.Name, "", false)
		if err != nil {
			return err
		}
		if rel != e.rel {
			p.idx = nil // every descendant moved
		}
		if err := p.writeFolder(rel, &f); err != nil {
			return err
		}
		out = &f
		return nil
	})
	return out, err
}

// DeleteFolder removes a folder with everything in it. An empty version
// deletes unconditionally.
func (s *Store) DeleteFolder(projectID, id, version string) error {
	return s.with(projectID, func(p *project) error {
		cur, e, err := p.loadFolder(id)
		if err != nil {
			return err
		}
		if err := checkDeleteVersion(KindFolder, cur.ID, version, cur.Version); err != nil {
			return err
		}
		p.idx = nil
		return removeAllRenamed(p.abs(e.rel))
	})
}

// GetAPI returns one API.
func (s *Store) GetAPI(projectID, id string) (*API, error) {
	var out *API
	err := s.with(projectID, func(p *project) (err error) {
		out, _, err = p.loadAPI(id)
		return err
	})
	return out, err
}

// CreateAPI creates an API at the end of in.FolderID (empty for the top
// level). The ID and order are assigned.
func (s *Store) CreateAPI(projectID string, in *API) (*API, error) {
	var out *API
	err := s.with(projectID, func(p *project) error {
		a := *in
		if err := a.normalize(); err != nil {
			return err
		}
		parentRel, parentID, err := p.folderDir(in.FolderID)
		if err != nil {
			return withFolderField(err)
		}
		sibs, err := p.children(parentRel)
		if err != nil {
			return err
		}
		taken, err := p.takenNames(parentRel)
		if err != nil {
			return err
		}
		if a.Order, err = p.placeAt(sibs, -1); err != nil {
			return err
		}
		a.ID, a.FolderID = newID(), parentID
		rel := path.Join(parentRel, uniqueName(fileStem(a.Name), apiExt, taken))
		if err := p.writeAPI(rel, &a); err != nil {
			return err
		}
		p.setIndex(a.ID, entry{kind: entryAPI, rel: rel, parent: parentID})
		out = &a
		return nil
	})
	return out, err
}

func withFolderField(err error) error {
	var nf *NotFoundError
	if errors.As(err, &nf) {
		return invalid("folder_id", "文件夹 %q 不存在", nf.ID)
	}
	return err
}

// UpdateAPI replaces the content of in.ID (everything except ID, order and
// folder, which Move changes). Renaming renames the file. in.Version must be
// the version the client read.
func (s *Store) UpdateAPI(projectID string, in *API) (*API, error) {
	var out *API
	err := s.with(projectID, func(p *project) error {
		cur, e, err := p.loadAPI(in.ID)
		if err != nil {
			return err
		}
		if err := checkVersion(KindAPI, cur.ID, in.Version, cur.Version); err != nil {
			return err
		}
		a := *in
		if err := a.normalize(); err != nil {
			return err
		}
		a.ID, a.Order, a.FolderID = cur.ID, cur.Order, cur.FolderID
		rel, err := p.renameFor(e.rel, dirOf(e.rel), cur.Name, a.Name, apiExt, false)
		if err != nil {
			return err
		}
		p.setIndex(a.ID, entry{kind: entryAPI, rel: rel, parent: e.parent})
		if err := p.writeAPI(rel, &a); err != nil {
			return err
		}
		out = &a
		return nil
	})
	return out, err
}

// DeleteAPI removes an API. An empty version deletes unconditionally.
func (s *Store) DeleteAPI(projectID, id, version string) error {
	return s.with(projectID, func(p *project) error {
		cur, e, err := p.loadAPI(id)
		if err != nil {
			return err
		}
		if err := checkDeleteVersion(KindAPI, cur.ID, version, cur.Version); err != nil {
			return err
		}
		if err := os.Remove(p.abs(e.rel)); err != nil {
			return fmt.Errorf("删除接口失败：%w", err)
		}
		delete(p.idx, cur.ID)
		return nil
	})
}

// copySuffix is appended to the name of a copied API.
const copySuffix = " 副本"

// CopyAPI duplicates an API under a new ID, placed right after the
// original in the same folder.
func (s *Store) CopyAPI(projectID, id string) (*API, error) {
	var out *API
	err := s.with(projectID, func(p *project) error {
		cur, e, err := p.loadAPI(id)
		if err != nil {
			return err
		}
		dir := dirOf(e.rel)
		sibs, err := p.children(dir)
		if err != nil {
			return err
		}
		pos := len(sibs)
		for i, sb := range sibs {
			if sb.id == cur.ID {
				pos = i + 1
				break
			}
		}
		taken, err := p.takenNames(dir)
		if err != nil {
			return err
		}
		a := *cur
		if a.Order, err = p.placeAt(sibs, pos); err != nil {
			return err
		}
		name := cur.Name
		if n := utf8.RuneCountInString(name) + utf8.RuneCountInString(copySuffix); n > maxNameRunes {
			name = string([]rune(name)[:maxNameRunes-utf8.RuneCountInString(copySuffix)])
		}
		a.ID, a.Name = newID(), name+copySuffix
		rel := path.Join(dir, uniqueName(fileStem(a.Name), apiExt, taken))
		if err := p.writeAPI(rel, &a); err != nil {
			return err
		}
		p.setIndex(a.ID, entry{kind: entryAPI, rel: rel, parent: e.parent})
		out = &a
		return nil
	})
	return out, err
}

// Move moves a folder or API to position req.Index of folder req.ParentID.
// Moving within the same folder reorders. Moving to another folder moves
// the file or directory, adding a suffix to its name on collision.
func (s *Store) Move(projectID string, req MoveRequest) (*MoveResult, error) {
	var out *MoveResult
	err := s.with(projectID, func(p *project) error {
		nid, ok := normalizeID(req.ID)
		if !ok {
			return notFound(KindNode, req.ID)
		}
		if p.idx == nil {
			p.scan()
		}
		e, ok := p.idx[nid]
		if !ok {
			p.scan()
			if e, ok = p.idx[nid]; !ok {
				return notFound(KindNode, req.ID)
			}
		}
		var (
			folder *Folder
			api    *API
			err    error
		)
		if e.kind == entryFolder {
			folder, e, err = p.loadFolder(nid)
		} else {
			api, e, err = p.loadAPI(nid)
		}
		if err != nil {
			return err
		}
		curVersion, name, ext := "", "", apiExt
		if folder != nil {
			curVersion, name, ext = folder.Version, folder.Name, ""
		} else {
			curVersion, name = api.Version, api.Name
		}
		if err := checkVersion(e.kind.kind(), nid, req.Version, curVersion); err != nil {
			return err
		}
		targetRel, targetID, err := p.folderDir(req.ParentID)
		if err != nil {
			return withParentField(err)
		}
		if folder != nil && (targetRel == e.rel || strings.HasPrefix(targetRel, e.rel+"/")) {
			return invalid("parent_id", "不能把文件夹移动到它自己或它的子文件夹里")
		}
		sibs, err := p.children(targetRel)
		if err != nil {
			return err
		}
		others := sibs[:0]
		for _, sb := range sibs {
			if sb.id != nid {
				others = append(others, sb)
			}
		}
		order, err := p.placeAt(others, req.Index)
		if err != nil {
			return err
		}
		rel := e.rel
		if dirOf(e.rel) != targetRel {
			if rel, err = p.renameFor(e.rel, targetRel, name, name, ext, true); err != nil {
				return err
			}
		}
		res := &MoveResult{ID: nid, ParentID: targetID, Order: order}
		if folder != nil {
			folder.Order, folder.ParentID = order, targetID
			if err := p.writeFolder(rel, folder); err != nil {
				return err
			}
			res.Type, res.Version = NodeFolder, folder.Version
			p.idx = nil
		} else {
			api.Order, api.FolderID = order, targetID
			if err := p.writeAPI(rel, api); err != nil {
				return err
			}
			res.Type, res.Version = NodeAPI, api.Version
			p.setIndex(nid, entry{kind: entryAPI, rel: rel, parent: targetID})
		}
		out = res
		return nil
	})
	return out, err
}
