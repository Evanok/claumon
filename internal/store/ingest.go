package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Session files pushed by agents (multi-machine mode) are appended to the
// machines directory chunk by chunk. ingest_offsets records, per machine and
// file, the offset in the agent's source file up to which data has been
// received, so an interrupted upload resumes without gaps or duplicates.

func migrateIngest(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS ingest_offsets (
			machine TEXT NOT NULL,
			path TEXT NOT NULL,
			source_offset INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (machine, path)
		);
	`)
	return err
}

// IngestOffset returns the source offset received for a machine's file, 0
// when nothing was received yet.
func (s *Store) IngestOffset(machine, path string) (int64, error) {
	var offset int64
	err := s.db.QueryRow("SELECT source_offset FROM ingest_offsets WHERE machine = ? AND path = ?", machine, path).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading ingest offset of %s/%s: %w", machine, path, err)
	}
	return offset, nil
}

// SetIngestOffset records the source offset received for a machine's file.
func (s *Store) SetIngestOffset(machine, path string, offset int64) error {
	_, err := s.db.Exec(`
		INSERT INTO ingest_offsets (machine, path, source_offset) VALUES (?, ?, ?)
		ON CONFLICT(machine, path) DO UPDATE SET source_offset = excluded.source_offset
	`, machine, path, offset)
	if err != nil {
		return fmt.Errorf("writing ingest offset of %s/%s: %w", machine, path, err)
	}
	return nil
}
