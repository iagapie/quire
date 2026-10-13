package query

import (
	"testing"
	"time"
)

func TestColumnKind(t *testing.T) {
	for typ, want := range map[string]string{
		"INT4": KindNumber, "Nullable(Int32)": KindNumber, "LowCardinality(Nullable(String))": KindText,
		"Array(Int64)": KindJSON, "Map(String, UInt8)": KindJSON, "BIGINT[]": KindJSON, "INTEGER[3]": KindJSON,
		`STRUCT("k" BIGINT)`: KindJSON, "Nullable(DateTime64(3))": KindDateTime, "Date32": KindDateTime,
		"INTERVAL": KindText, "RAW(20)": KindBinary, "BOOL": KindBool, "JSONB": KindJSON, "BYTEA": KindBinary, "UUID": KindText,
	} {
		if got := columnKind(typ); got != want {
			t.Errorf("columnKind(%q) = %s, want %s", typ, got, want)
		}
	}
}

func TestFormatDate(t *testing.T) {
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if got := formatTime("DATE", day); got != "2026-10-08" {
		t.Errorf("date = %s", got)
	}
	if got := formatTime("DATE", day.Add(14*time.Hour+5*time.Minute)); got != "2026-10-08 14:05:00" {
		t.Errorf("Oracle date with a time = %s", got)
	}
}
