package store

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"tidegate/internal/scenario"
)

// MaxFileSize bounds uploaded data files.
const MaxFileSize = 512 << 20

// DataFile describes an uploaded data file.
type DataFile struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	// Version identifies the file content for optimistic concurrency. It is
	// derived from size and modification time, so that listing does not
	// read every file.
	Version string `json:"version"`
}

func dataFileOf(name string, fi os.FileInfo) *DataFile {
	return &DataFile{
		Name:    name,
		Size:    fi.Size(),
		ModTime: fi.ModTime().UTC(),
		Version: versionOf([]byte(strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10))),
	}
}

func (p *project) filesDir() string { return filepath.Join(p.dir, filesDir) }

// statFile returns the path and description of an existing data file.
func (p *project) statFile(name string) (string, *DataFile, error) {
	path, err := joinInside(p.filesDir(), name)
	if err != nil {
		return "", nil, err
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return "", nil, notFound(KindFile, name)
		}
		return "", nil, err
	}
	return path, dataFileOf(name, fi), nil
}

// FilesDir returns the data file directory of a project.
func (s *Store) FilesDir(projectID string) (string, error) {
	var dir string
	err := s.with(projectID, func(p *project) error {
		dir = p.filesDir()
		return nil
	})
	return dir, err
}

// ListFiles returns the data files of a project sorted by name.
func (s *Store) ListFiles(projectID string) ([]*DataFile, error) {
	out := []*DataFile{}
	err := s.with(projectID, func(p *project) error {
		des, err := os.ReadDir(p.filesDir())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("读取数据文件目录失败：%w", err)
		}
		for _, de := range des {
			if !de.Type().IsRegular() || ValidateFileName(de.Name()) != nil {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				continue
			}
			out = append(out, dataFileOf(de.Name(), fi))
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, err
}

// StatFile describes one data file.
func (s *Store) StatFile(projectID, name string) (*DataFile, error) {
	var out *DataFile
	err := s.with(projectID, func(p *project) (err error) {
		_, out, err = p.statFile(name)
		return err
	})
	return out, err
}

// FilePath returns the absolute path of an existing data file. The name is
// validated, so the path is always inside the project's file directory.
func (s *Store) FilePath(projectID, name string) (string, error) {
	var out string
	err := s.with(projectID, func(p *project) (err error) {
		out, _, err = p.statFile(name)
		return err
	})
	return out, err
}

// SaveFile stores an uploaded data file. With an empty version the file
// must not exist yet; to replace a file pass the version the client read.
// The upload is streamed to a temporary file without holding the project
// lock, then renamed into place.
func (s *Store) SaveFile(projectID, name string, r io.Reader, version string) (*DataFile, error) {
	if err := ValidateFileName(name); err != nil {
		return nil, err
	}
	var dir string
	if err := s.with(projectID, func(p *project) error {
		dir = p.filesDir()
		return os.MkdirAll(dir, dirPerm)
	}); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, tempPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("创建临时文件失败：%w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(r, MaxFileSize+1))
	if err == nil && n > MaxFileSize {
		err = invalid("file", "文件超过 %d MB", MaxFileSize>>20)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			return nil, err
		}
		return nil, fmt.Errorf("保存数据文件失败：%w", err)
	}

	var out *DataFile
	err = s.with(projectID, func(p *project) error {
		target, err := joinInside(p.filesDir(), name)
		if err != nil {
			return err
		}
		fi, err := os.Stat(target)
		switch {
		case err == nil && !fi.Mode().IsRegular():
			return invalid("name", "%q 已被占用", name)
		case err == nil && version == "":
			return &ConflictError{Kind: KindFile, ID: name, Current: dataFileOf(name, fi).Version, Msg: fmt.Sprintf("数据文件 %q 已存在", name)}
		case err == nil && version != dataFileOf(name, fi).Version:
			return &ConflictError{Kind: KindFile, ID: name, Current: dataFileOf(name, fi).Version}
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		case err != nil && version != "":
			return &ConflictError{Kind: KindFile, ID: name, Msg: fmt.Sprintf("数据文件 %q 已被删除，请刷新后重试", name)}
		}
		if err := renameRetry(tmpName, target); err != nil {
			return fmt.Errorf("保存数据文件失败：%w", err)
		}
		committed = true
		syncDir(p.filesDir())
		fi, err = os.Stat(target)
		if err != nil {
			return err
		}
		out = dataFileOf(name, fi)
		return nil
	})
	return out, err
}

// DeleteFile removes a data file. An empty version deletes unconditionally.
func (s *Store) DeleteFile(projectID, name, version string) error {
	return s.with(projectID, func(p *project) error {
		path, df, err := p.statFile(name)
		if err != nil {
			return err
		}
		if err := checkDeleteVersion(KindFile, name, version, df.Version); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("删除数据文件失败：%w", err)
		}
		return nil
	})
}

// FilePreview summarizes a data file's structure and initial rows for the UI.
type FilePreview struct {
	Name      string     `json:"name"`
	Size      int64      `json:"size"`
	IsCSV     bool       `json:"is_csv"`
	Columns   []string   `json:"columns,omitempty"`
	Rows      [][]string `json:"rows,omitempty"`
	TotalRows int        `json:"total_rows,omitempty"`
	Text      string     `json:"text,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

// PreviewFile returns up to maxRows preview rows for CSV files (default 20,
// max 100) or a text snippet for non-CSV files.
func (s *Store) PreviewFile(projectID, name string, maxRows int) (*FilePreview, error) {
	if maxRows <= 0 {
		maxRows = 20
	}
	if maxRows > 100 {
		maxRows = 100
	}
	var out *FilePreview
	err := s.with(projectID, func(p *project) error {
		path, df, err := p.statFile(name)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("读取数据文件失败：%w", err)
		}
		defer f.Close()

		pv := &FilePreview{Name: df.Name, Size: df.Size}
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".csv" {
			pv.IsCSV = true
			// Strip optional UTF-8 BOM without loading the whole file into memory.
			var bom [3]byte
			n, _ := io.ReadFull(f, bom[:])
			var r io.Reader
			if n == 3 && bytes.Equal(bom[:], utf8BOM) {
				r = f
			} else {
				r = io.MultiReader(bytes.NewReader(bom[:n]), f)
			}
			cr := csv.NewReader(r)
			cr.ReuseRecord = false
			cols, err := cr.Read()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return invalid("file", "CSV 文件为空，首行必须是列名")
				}
				return invalid("file", "CSV 格式错误：%v", err)
			}
			for i := range cols {
				cols[i] = strings.TrimSpace(cols[i])
			}
			pv.Columns = cols
			cr.FieldsPerRecord = len(cols)
			for {
				rec, err := cr.Read()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return invalid("file", "CSV 第 %d 行格式错误：%v", pv.TotalRows+2, err)
				}
				if len(pv.Rows) < maxRows {
					pv.Rows = append(pv.Rows, rec)
				}
				pv.TotalRows++
			}
			out = pv
			return nil
		}
		if ext == ".json" || ext == ".jsonl" || ext == ".ndjson" {
			if cols, rows, lerr := scenario.LoadDataFile(path); lerr == nil {
				pv.IsCSV = true
				pv.Columns = cols
				pv.TotalRows = len(rows)
				if len(rows) > maxRows {
					pv.Rows = rows[:maxRows]
				} else {
					pv.Rows = rows
				}
				out = pv
				return nil
			} else if ext != ".json" {
				return invalid("file", "解析数据文件失败：%v", lerr)
			}
			// Single JSON object or arbitrary JSON text falls through to text snippet preview.
		}

		const maxSnippet = 4096
		buf := make([]byte, maxSnippet+1)
		n, err := io.ReadFull(f, buf)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("读取数据文件失败：%w", err)
		}
		buf = buf[:n]
		if n > maxSnippet {
			buf = buf[:maxSnippet]
			pv.Truncated = true
		}
		if utf8.Valid(buf) {
			pv.Text = string(buf)
		}
		out = pv
		return nil
	})
	return out, err
}
