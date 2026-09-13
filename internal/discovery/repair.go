package discovery

import (
	"context"
	"log/slog"

	"github.com/Net005/JAVBeacon/internal/domain"
)

type RankRepairStore interface {
	AllDiscoveryAIRanks(context.Context) ([]domain.DiscoveryAIRank, error)
	DeleteDiscoveryAIRanks(context.Context, []int64) (int64, error)
}

// RepairStoredRanks is safe and idempotent. It touches only malformed AI rank
// rows; deleting the row also deletes its fingerprint association, so the
// ordinary enrichment miss path automatically schedules the release again.
func RepairStoredRanks(ctx context.Context, st RankRepairStore, pools string, log *slog.Logger) (int64, error) {
	ranks, err := st.AllDiscoveryAIRanks(ctx)
	if err != nil {
		return 0, err
	}
	invalid := make([]int64, 0)
	for _, rank := range ranks {
		if ValidateStoredRank(rank, pools) != nil {
			invalid = append(invalid, rank.ReleaseID)
		}
	}
	removed, err := st.DeleteDiscoveryAIRanks(ctx, invalid)
	if err != nil {
		return 0, err
	}
	if log != nil {
		log.Info("AI Discovery repair: malformed rankings invalidated", "count", removed)
	}
	return removed, nil
}
