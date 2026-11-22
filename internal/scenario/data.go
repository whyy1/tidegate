package scenario

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DataMode says how virtual users pick rows of a dataset.
type DataMode uint8

const (
	// DataSequential hands out rows in order across all virtual users, one
	// row per iteration, wrapping at the end.
	DataSequential DataMode = iota
	// DataRandom picks a random row per iteration.
	DataRandom
	// DataUnique binds one row to each virtual user for its lifetime.
	DataUnique
)

func (m DataMode) String() string {
	switch m {
	case DataRandom:
		return "random"
	case DataUnique:
		return "unique"
	}
	return "sequential"
}

// Dataset is a loaded data file.
type Dataset struct {
	Name    string
	Mode    DataMode
	Columns []string
	Rows    [][]string
	// Used reports whether any template references the dataset; unused
	// datasets cost nothing at run time.
	Used bool
}

// Column returns the index of a column or -1.
func (d *Dataset) Column(name string) int {
	for i, c := range d.Columns {
		if c == name {
			return i
		}
	}
	return -1
}

const maxDataFileSize = 512 << 20

// LoadCSV reads a CSV file whose first record holds the column names.
func LoadCSV(path string) (columns []string, rows [][]string, err error) {
	data, err := readBoundedDataFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParseCSV(data)
}

// IsSupportedDataExt reports whether a file extension is a supported parameterized data source.
func IsSupportedDataExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".csv", ".json", ".jsonl", ".ndjson":
		return true
	}
	return false
}

// LoadDataFile reads CSV, JSON array, or JSONL/NDJSON data files into named columns and rows.
func LoadDataFile(path string) (columns []string, rows [][]string, err error) {
	data, err := readBoundedDataFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParseDataFile(path, data)
}

func readBoundedDataFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > maxDataFileSize {
		return nil, fmt.Errorf("文件超过 %d MB", maxDataFileSize>>20)
	}
	return io.ReadAll(f)
}

// ParseDataFile parses CSV, JSON array-of-objects, or JSONL/NDJSON data.
func ParseDataFile(filename string, data []byte) ([]string, [][]string, error) {
	clean := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	trimmed := bytes.TrimSpace(clean)
	ext := strings.ToLower(pathExt(filename))
	switch ext {
	case ".jsonl", ".ndjson":
		return parseJSONLines(trimmed)
	case ".json":
		return parseJSONArray(trimmed)
	default:
		if ext == "" && bytes.HasPrefix(trimmed, []byte("[")) {
			return parseJSONArray(trimmed)
		}
		return ParseCSV(clean)
	}
}

func parseJSONArray(data []byte) ([]string, [][]string, error) {
	if len(data) == 0 {
		return nil, nil, errors.New("JSON 数据文件为空")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var objs []map[string]any
	if err := dec.Decode(&objs); err != nil {
		return nil, nil, fmt.Errorf("JSON 数据文件必须是对象数组：%v", err)
	}
	return parseJSONObjects(objs)
}

func parseJSONLines(data []byte) ([]string, [][]string, error) {
	if len(data) == 0 {
		return nil, nil, errors.New("JSONL 数据文件为空")
	}
	var objs []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil || obj == nil {
			return nil, nil, fmt.Errorf("第 %d 行不是有效的 JSON 对象", lineNo)
		}
		objs = append(objs, obj)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	return parseJSONObjects(objs)
}

func parseJSONObjects(objs []map[string]any) ([]string, [][]string, error) {
	if len(objs) == 0 {
		return nil, nil, errors.New("数据文件至少需要 1 行数据对象")
	}
	colSet := map[string]bool{}
	var cols []string
	for _, obj := range objs {
		for k := range obj {
			if k == "" {
				continue
			}
			if !colSet[k] {
				colSet[k] = true
				cols = append(cols, k)
			}
		}
	}
	sort.Strings(cols)
	if len(cols) == 0 {
		return nil, nil, errors.New("数据对象缺少有效字段名")
	}
	rows := make([][]string, len(objs))
	for i, obj := range objs {
		row := make([]string, len(cols))
		for j, col := range cols {
			row[j] = stringifyJSONCell(obj[col])
		}
		rows[i] = row
	}
	return cols, rows, nil
}

func stringifyJSONCell(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}

// ParseCSV parses CSV data whose first record holds the column names.
func ParseCSV(data []byte) (columns []string, rows [][]string, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(data))
	r.ReuseRecord = false
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, errors.New("文件为空，首行必须是列名")
	}
	if err != nil {
		return nil, nil, csvError(err)
	}
	seen := map[string]bool{}
	for i, c := range header {
		c = strings.TrimSpace(c)
		if c == "" {
			return nil, nil, fmt.Errorf("第 %d 列的列名为空", i+1)
		}
		if seen[c] {
			return nil, nil, fmt.Errorf("列名 %q 重复", c)
		}
		seen[c] = true
		header[i] = c
	}
	r.FieldsPerRecord = len(header)
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, csvError(err)
		}
		rows = append(rows, rec)
	}
	if len(rows) == 0 {
		return nil, nil, errors.New("只有列名，没有数据行")
	}
	return header, rows, nil
}

func csvError(err error) error {
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		if errors.Is(pe.Err, csv.ErrFieldCount) {
			return fmt.Errorf("第 %d 行的列数和首行不一致", pe.Line)
		}
		return fmt.Errorf("第 %d 行格式错误：%v", pe.Line, pe.Err)
	}
	return err
}

func resolvePath(baseDir, file string) string {
	if filepath.IsAbs(file) || baseDir == "" {
		return file
	}
	return filepath.Join(baseDir, file)
}
