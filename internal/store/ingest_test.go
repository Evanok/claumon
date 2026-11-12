package store

import "testing"

func TestIngestOffsets(t *testing.T) {
	st := openTestStore(t)

	if got, err := st.IngestOffset("office", "projects/-p/s1.jsonl"); err != nil || got != 0 {
		t.Fatalf("unknown file: offset = %d, %v; want 0, nil", got, err)
	}
	for _, w := range []struct {
		machine, path string
		offset        int64
	}{
		{"office", "projects/-p/s1.jsonl", 100},
		{"home", "projects/-p/s1.jsonl", 7},
		{"office", "projects/-p/s1.jsonl", 250}, // overwrites
	} {
		if err := st.SetIngestOffset(w.machine, w.path, w.offset); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		machine string
		want    int64
	}{{"office", 250}, {"home", 7}} {
		if got, err := st.IngestOffset(tt.machine, "projects/-p/s1.jsonl"); err != nil || got != tt.want {
			t.Errorf("%s: offset = %d, %v; want %d", tt.machine, got, err, tt.want)
		}
	}
}
