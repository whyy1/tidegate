package scenario

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if st.Size() > maxDataFileSize {
		return nil, nil, fmt.Errorf("文件超过 %d MB", maxDataFileSize>>20)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return ParseCSV(data)
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
