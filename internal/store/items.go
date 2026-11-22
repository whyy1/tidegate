package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Environments and scenarios are stored as <dir>/<ID>.yaml.

func (p *project) itemPath(sub, id string) string {
	return filepath.Join(p.dir, sub, id+".yaml")
}

// itemIDs returns the IDs of the <ID>.yaml files in a subdirectory, in
// creation order.
func (p *project) itemIDs(sub string) ([]string, error) {
	des, err := os.ReadDir(filepath.Join(p.dir, sub))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取目录失败：%w", err)
	}
	var ids []string
	for _, de := range des {
		if !de.Type().IsRegular() {
			continue
		}
		stem, ok := strings.CutSuffix(de.Name(), ".yaml")
		if !ok {
			continue
		}
		if id, ok := normalizeID(stem); ok && id == stem {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// readItem decodes <sub>/<id>.yaml and returns the canonical ID and version.
func (p *project) readItem(k Kind, sub, id string, v any) (string, string, error) {
	nid, ok := normalizeID(id)
	if !ok {
		return "", "", notFound(k, id)
	}
	ver, err := readYAML(p.itemPath(sub, nid), v)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", notFound(k, id)
		}
		return "", "", err
	}
	return nid, ver, nil
}

func (p *project) writeItem(sub, id string, v any) (string, error) {
	if err := os.MkdirAll(filepath.Join(p.dir, sub), dirPerm); err != nil {
		return "", fmt.Errorf("创建目录失败：%w", err)
	}
	return writeYAML(p.itemPath(sub, id), v)
}

func (p *project) removeItem(k Kind, sub, id string) error {
	if err := os.Remove(p.itemPath(sub, id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return notFound(k, id)
		}
		return fmt.Errorf("删除%s失败：%w", k.Label(), err)
	}
	return nil
}

func (p *project) readEnvironment(id string) (*Environment, error) {
	var e Environment
	nid, ver, err := p.readItem(KindEnvironment, environmentsDir, id, &e)
	if err != nil {
		return nil, err
	}
	e.ID, e.Version = nid, ver
	return &e, nil
}

// ListEnvironments returns the environments of a project in creation
// order. Files that cannot be parsed are skipped.
func (s *Store) ListEnvironments(projectID string) ([]*Environment, error) {
	out := []*Environment{}
	err := s.with(projectID, func(p *project) error {
		ids, err := p.itemIDs(environmentsDir)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if e, err := p.readEnvironment(id); err == nil {
				out = append(out, e)
			}
		}
		return nil
	})
	return out, err
}

// GetEnvironment returns one environment.
func (s *Store) GetEnvironment(projectID, id string) (*Environment, error) {
	var out *Environment
	err := s.with(projectID, func(p *project) (err error) {
		out, err = p.readEnvironment(id)
		return err
	})
	return out, err
}

// CreateEnvironment creates an environment; the ID is assigned.
func (s *Store) CreateEnvironment(projectID string, in *Environment) (*Environment, error) {
	var out *Environment
	err := s.with(projectID, func(p *project) error {
		e := *in
		if err := e.normalize(); err != nil {
			return err
		}
		e.ID = newID()
		v, err := p.writeItem(environmentsDir, e.ID, &e)
		if err != nil {
			return err
		}
		e.Version = v
		out = &e
		return nil
	})
	return out, err
}

// UpdateEnvironment replaces an environment. in.Version must be the version
// the client read.
func (s *Store) UpdateEnvironment(projectID string, in *Environment) (*Environment, error) {
	var out *Environment
	err := s.with(projectID, func(p *project) error {
		cur, err := p.readEnvironment(in.ID)
		if err != nil {
			return err
		}
		if err := checkVersion(KindEnvironment, cur.ID, in.Version, cur.Version); err != nil {
			return err
		}
		e := *in
		if err := e.normalize(); err != nil {
			return err
		}
		e.ID = cur.ID
		if e.Version, err = p.writeItem(environmentsDir, e.ID, &e); err != nil {
			return err
		}
		out = &e
		return nil
	})
	return out, err
}

// DeleteEnvironment removes an environment. An empty version deletes
// unconditionally.
func (s *Store) DeleteEnvironment(projectID, id, version string) error {
	return s.with(projectID, func(p *project) error {
		cur, err := p.readEnvironment(id)
		if err != nil {
			return err
		}
		if err := checkDeleteVersion(KindEnvironment, cur.ID, version, cur.Version); err != nil {
			return err
		}
		return p.removeItem(KindEnvironment, environmentsDir, cur.ID)
	})
}

func (p *project) readScenario(id string) (*Scenario, error) {
	var sc Scenario
	nid, ver, err := p.readItem(KindScenario, scenariosDir, id, &sc)
	if err != nil {
		return nil, err
	}
	sc.ID, sc.Version = nid, ver
	return &sc, nil
}

// prepareScenario copies, validates and normalizes a scenario and checks
// that the APIs of enabled steps and the default environment exist.
func (p *project) prepareScenario(in *Scenario) (*Scenario, error) {
	sc := *in
	sc.Steps = cloneScenarioSteps(in.Steps)
	if err := sc.normalize(); err != nil {
		return nil, err
	}
	if err := p.validateScenarioAPIs("steps", sc.Steps); err != nil {
		return nil, err
	}
	if sc.EnvironmentID != "" {
		if _, err := p.readEnvironment(sc.EnvironmentID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, invalid("environment_id", "环境 %s 不存在", sc.EnvironmentID)
			}
			return nil, err
		}
	}
	return &sc, nil
}

func (p *project) validateScenarioAPIs(prefix string, steps []ScenarioStep) error {
	for i := range steps {
		st := &steps[i]
		if st.Disabled {
			continue
		}
		field := fmt.Sprintf("%s[%d]", prefix, i)
		stType := strings.ToLower(strings.TrimSpace(st.Type))
		switch stType {
		case "", "http":
			if _, _, err := p.loadAPI(st.APIID); err != nil {
				if errors.Is(err, ErrNotFound) {
					return invalid(field+".api_id", "引用的接口 %s 不存在", st.APIID)
				}
				return err
			}
		case "group", "loop":
			if err := p.validateScenarioAPIs(field+".children", st.Children); err != nil {
				return err
			}
		case "if":
			if err := p.validateScenarioAPIs(field+".children", st.Children); err != nil {
				return err
			}
			if err := p.validateScenarioAPIs(field+".else", st.Else); err != nil {
				return err
			}
		case "weighted":
			for bIdx := range st.Branches {
				if err := p.validateScenarioAPIs(fmt.Sprintf("%s.branches[%d].steps", field, bIdx), st.Branches[bIdx].Steps); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ListScenarios returns the scenarios of a project in creation order.
// Files that cannot be parsed are skipped.
func (s *Store) ListScenarios(projectID string) ([]*Scenario, error) {
	out := []*Scenario{}
	err := s.with(projectID, func(p *project) error {
		ids, err := p.itemIDs(scenariosDir)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if sc, err := p.readScenario(id); err == nil {
				out = append(out, sc)
			}
		}
		return nil
	})
	return out, err
}

// GetScenario returns one scenario.
func (s *Store) GetScenario(projectID, id string) (*Scenario, error) {
	var out *Scenario
	err := s.with(projectID, func(p *project) (err error) {
		out, err = p.readScenario(id)
		return err
	})
	return out, err
}

// CreateScenario creates a scenario; the ID is assigned and steps without an
// ID get one.
func (s *Store) CreateScenario(projectID string, in *Scenario) (*Scenario, error) {
	var out *Scenario
	err := s.with(projectID, func(p *project) error {
		sc, err := p.prepareScenario(in)
		if err != nil {
			return err
		}
		sc.ID = newID()
		if sc.Version, err = p.writeItem(scenariosDir, sc.ID, sc); err != nil {
			return err
		}
		out = sc
		return nil
	})
	return out, err
}

// UpdateScenario replaces a scenario. in.Version must be the version the
// client read.
func (s *Store) UpdateScenario(projectID string, in *Scenario) (*Scenario, error) {
	var out *Scenario
	err := s.with(projectID, func(p *project) error {
		cur, err := p.readScenario(in.ID)
		if err != nil {
			return err
		}
		if err := checkVersion(KindScenario, cur.ID, in.Version, cur.Version); err != nil {
			return err
		}
		sc, err := p.prepareScenario(in)
		if err != nil {
			return err
		}
		sc.ID = cur.ID
		if sc.Version, err = p.writeItem(scenariosDir, sc.ID, sc); err != nil {
			return err
		}
		out = sc
		return nil
	})
	return out, err
}

// DeleteScenario removes a scenario. An empty version deletes
// unconditionally.
func (s *Store) DeleteScenario(projectID, id, version string) error {
	return s.with(projectID, func(p *project) error {
		cur, err := p.readScenario(id)
		if err != nil {
			return err
		}
		if err := checkDeleteVersion(KindScenario, cur.ID, version, cur.Version); err != nil {
			return err
		}
		return p.removeItem(KindScenario, scenariosDir, cur.ID)
	})
}
