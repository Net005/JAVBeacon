package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

type repairStore struct {
	ranks map[int64]domain.DiscoveryAIRank
}

func (s *repairStore) AllDiscoveryAIRanks(context.Context) ([]domain.DiscoveryAIRank, error) {
	result := make([]domain.DiscoveryAIRank, 0, len(s.ranks))
	for _, rank := range s.ranks {
		result = append(result, rank)
	}
	return result, nil
}

func (s *repairStore) DeleteDiscoveryAIRanks(_ context.Context, ids []int64) (int64, error) {
	var removed int64
	for _, id := range ids {
		if _, ok := s.ranks[id]; ok {
			delete(s.ranks, id)
			removed++
		}
	}
	return removed, nil
}

func TestExistingInvalidAIRankingRepair(t *testing.T) {
	st := &repairStore{ranks: map[int64]domain.DiscoveryAIRank{
		1: {ReleaseID: 1, Score: 70, Reason: actualBadReason, Fingerprint: "unsafe-v1", GeneratedAt: time.Now()},
		2: {ReleaseID: 2, Score: 88, Reason: "Match: Its science-fiction setting aligns strongly with the established preference for related themes.", Pools: []string{"Sci-Fi"}, Fingerprint: "valid", GeneratedAt: time.Now()},
	}}
	removed, err := RepairStoredRanks(context.Background(), st, "Sci-Fi | space", nil)
	if err != nil || removed != 1 {
		t.Fatalf("repair removed %d, err=%v", removed, err)
	}
	if _, exists := st.ranks[1]; exists {
		t.Fatal("invalid ranking and fingerprint association survived repair")
	}
	if _, exists := st.ranks[2]; !exists {
		t.Fatal("valid ranking was removed")
	}
	removed, err = RepairStoredRanks(context.Background(), st, "Sci-Fi | space", nil)
	if err != nil || removed != 0 {
		t.Fatalf("repair was not idempotent: removed=%d err=%v", removed, err)
	}
}

func TestInvalidatedRankingBecomesEligibleForReranking(t *testing.T) {
	st := &repairStore{ranks: map[int64]domain.DiscoveryAIRank{7: {ReleaseID: 7, Score: 60, Reason: actualBadReason, Fingerprint: "old"}}}
	if _, err := RepairStoredRanks(context.Background(), st, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, enriched := st.ranks[7]; enriched {
		t.Fatal("deleted ranking would still be treated as enriched")
	}
}
