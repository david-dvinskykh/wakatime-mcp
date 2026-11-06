package main

import (
	"bytes"
	"encoding/csv"
	"io"
	"strconv"
)

// writeCSV writes rows with the header project,branch,start,seconds.
func writeCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"project", "branch", "start", "seconds"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{r.Project, r.Branch, r.Start, strconv.FormatInt(r.Seconds, 10)}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func csvString(rows []Row) (string, error) {
	var buf bytes.Buffer
	err := writeCSV(&buf, rows)
	return buf.String(), err
}
