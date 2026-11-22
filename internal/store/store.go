// Package store keeps Tidegate's data: project files (YAML, one file per
// API, environment and scenario, friendly to version control) and the result
// database (SQLite) with runs, per-second snapshots, samples, alerts and
// settings.
//
// Project layout under <data>/projects/<projectID>/:
//
//	project.yaml                 name and description
//	environments/<id>.yaml       base URL and variables
//	apis/                        folder tree; every directory is a folder
//	  _folder.yaml               folder ID, name, order, inherited headers and auth
//	  <name>.api.yaml            one API per file
//	scenarios/<id>.yaml          steps referencing API IDs, load and HTTP options
//	files/                       uploaded data files
//
// Every path is derived on the server from IDs and validated names; nothing
// accepts a path from a client. Writes go to a temporary file that is then
// renamed over the target, and every update carries the version (content
// hash) the client read, so concurrent edits are detected.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	projectFile     = "project.yaml"
	environmentsDir = "environments"
	scenariosDir    = "scenarios"
	filesDir        = "files"
)

// Store manages the project files under <data>/projects. It is safe for
// concurrent use: operations on one project are serialized by a per-project
// lock, operations on different projects run in parallel.
type Store struct {
	root string

	mu       sync.Mutex
	projects map[string]*project
}

// project is the in-memory state of one project directory. All fields
// except id and dir are guarded by mu.
type project struct {
	id  string
	dir string

	mu      sync.Mutex
	deleted bool
	// idx maps folder and API IDs to their location; nil until the first
	// scan. It is a cache: every lookup verifies the file it points to and
	// rescans on a mismatch, so edits made outside the server are picked up.
	idx map[string]entry
}

// Open opens (creating if needed) the project store under dataDir.
func Open(dataDir string) (*Store, error) {
	root := filepath.Join(dataDir, "projects")
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("创建项目目录失败：%w", err)
	}
	// Remove leftovers of interrupted creates and deletes.
	if des, err := os.ReadDir(root); err == nil {
		for _, de := range des {
			if n := de.Name(); strings.HasPrefix(n, tempPrefix) || strings.HasPrefix(n, trashPrefix) {
				_ = os.RemoveAll(filepath.Join(root, n))
			}
		}
	}
	return &Store{root: root, projects: map[string]*project{}}, nil
}

// Root returns the directory that holds the projects.
func (s *Store) Root() string { return s.root }

// acquire returns the project with its lock held. The caller must call
// p.mu.Unlock.
func (s *Store) acquire(id string) (*project, error) {
	nid, ok := normalizeID(id)
	if !ok {
		return nil, notFound(KindProject, id)
	}
	dir := filepath.Join(s.root, nid)
	s.mu.Lock()
	p := s.projects[nid]
	if p == nil {
		if _, err := os.Stat(filepath.Join(dir, projectFile)); err != nil {
			s.mu.Unlock()
			return nil, notFound(KindProject, id)
		}
		p = &project{id: nid, dir: dir}
		s.projects[nid] = p
	}
	s.mu.Unlock()
	p.mu.Lock()
	if p.deleted {
		p.mu.Unlock()
		return nil, notFound(KindProject, id)
	}
	if _, err := os.Stat(filepath.Join(dir, projectFile)); err != nil {
		p.mu.Unlock()
		return nil, notFound(KindProject, id)
	}
	return p, nil
}

// with runs fn with the project lock held.
func (s *Store) with(id string, fn func(p *project) error) error {
	p, err := s.acquire(id)
	if err != nil {
		return err
	}
	defer p.mu.Unlock()
	return fn(p)
}

func (p *project) readProject() (*Project, error) {
	var pr Project
	v, err := readYAML(filepath.Join(p.dir, projectFile), &pr)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, notFound(KindProject, p.id)
		}
		return nil, err
	}
	pr.ID, pr.Version = p.id, v
	return &pr, nil
}

// ListProjects returns all projects in creation order.
func (s *Store) ListProjects() ([]*Project, error) {
	des, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("读取项目目录失败：%w", err)
	}
	out := []*Project{}
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		if id, ok := normalizeID(de.Name()); !ok || id != de.Name() {
			continue
		}
		pr, err := s.GetProject(de.Name())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetProject returns one project.
func (s *Store) GetProject(id string) (*Project, error) {
	var pr *Project
	err := s.with(id, func(p *project) (err error) {
		pr, err = p.readProject()
		return err
	})
	return pr, err
}

func (pr *Project) normalize() error {
	name, err := cleanName("name", pr.Name)
	if err != nil {
		return err
	}
	pr.Name = name
	pr.Description = strings.TrimSpace(pr.Description)
	return validateVariablesList("variables", pr.Variables)
}

// CreateProject creates a project with its directory skeleton. The ID is
// generated; Name, Description and Variables of in are used.
func (s *Store) CreateProject(in *Project) (*Project, error) {
	pr := Project{Name: in.Name, Description: in.Description, Variables: append(KeyValueList(nil), in.Variables...)}
	if err := pr.normalize(); err != nil {
		return nil, err
	}
	pr.ID = newID()
	// Build the skeleton under a hidden name and rename it into place so
	// that a half created project is never listed.
	tmp := filepath.Join(s.root, tempPrefix+pr.ID)
	for _, sub := range []string{environmentsDir, apisDir, scenariosDir, filesDir} {
		if err := os.MkdirAll(filepath.Join(tmp, sub), dirPerm); err != nil {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("创建项目目录失败：%w", err)
		}
	}
	v, err := writeYAML(filepath.Join(tmp, projectFile), &pr)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if err := renameRetry(tmp, filepath.Join(s.root, pr.ID)); err != nil {
		os.RemoveAll(tmp)
		return nil, fmt.Errorf("创建项目目录失败：%w", err)
	}
	pr.Version = v
	return &pr, nil
}

// UpdateProject changes the name and description of in.ID. in.Version must
// be the version the client read.
func (s *Store) UpdateProject(in *Project) (*Project, error) {
	var out *Project
	err := s.with(in.ID, func(p *project) error {
		cur, err := p.readProject()
		if err != nil {
			return err
		}
		if err := checkVersion(KindProject, p.id, in.Version, cur.Version); err != nil {
			return err
		}
		pr := Project{ID: p.id, Name: in.Name, Description: in.Description, Variables: append(KeyValueList(nil), in.Variables...)}
		if err := pr.normalize(); err != nil {
			return err
		}
		if pr.Version, err = writeYAML(filepath.Join(p.dir, projectFile), &pr); err != nil {
			return err
		}
		out = &pr
		return nil
	})
	return out, err
}

// DeleteProject removes a project and everything in it. An empty version
// deletes unconditionally.
func (s *Store) DeleteProject(id, version string) error {
	p, err := s.acquire(id)
	if err != nil {
		return err
	}
	defer p.mu.Unlock()
	cur, err := p.readProject()
	if err != nil {
		return err
	}
	if err := checkDeleteVersion(KindProject, p.id, version, cur.Version); err != nil {
		return err
	}
	if err := removeAllRenamed(p.dir); err != nil {
		return err
	}
	p.deleted = true
	s.mu.Lock()
	delete(s.projects, p.id)
	s.mu.Unlock()
	return nil
}
