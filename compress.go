package main

import (
	"context"
	"log"
	"time"

	"github.com/fabioconcina/claumon/internal/ingest"
)

// compressInactiveLoop compresses the received session files inactive for
// olderThan, a minute after startup and then every hour.
func compressInactiveLoop(ctx context.Context, rc *ingest.Receiver, olderThan time.Duration) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		stats, err := rc.CompressInactive(olderThan)
		if stats.Files > 0 {
			log.Printf("[ingest] Compressed %d inactive session files: %.1f MB -> %.1f MB",
				stats.Files, float64(stats.Before)/(1<<20), float64(stats.After)/(1<<20))
		}
		if err != nil {
			log.Printf("[ingest] Compression stopped: %v", err)
		}
	}
}
