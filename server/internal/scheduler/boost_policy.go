package scheduler

import (
	"fmt"
	"math"
	"sort"
)

// BoostWordWithCapacity must run in the caller's transaction, so eviction and
// activation either both commit or both roll back. Repeated activation keeps age.
func (s *Service) BoostWordWithCapacity(sid, bid, wid int64, capacity int) ([]int64, error) {
	if capacity < 1 {
		return nil, fmt.Errorf("invalid boost capacity")
	}
	cards, err := s.repo.ListCards(sid, bid)
	if err != nil {
		return nil, err
	}
	var target *Card
	active := []*Card{}
	var sequence int64
	for _, c := range cards {
		if c.WordID == wid {
			target = c
		}
		if c.BoostSequence > sequence {
			sequence = c.BoostSequence
		}
		if c.BoostFlag {
			active = append(active, c)
		}
	}
	if target == nil {
		return nil, ErrCardNotFound
	}
	if target.BoostFlag {
		return []int64{}, nil
	}
	if sequence == math.MaxInt64 {
		return nil, fmt.Errorf("boost sequence exhausted")
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].BoostSequence == active[j].BoostSequence {
			return active[i].WordID < active[j].WordID
		}
		return active[i].BoostSequence < active[j].BoostSequence
	})
	evicted := []int64{}
	for len(active) >= capacity {
		old := active[0]
		active = active[1:]
		old.BoostFlag = false
		if err := s.repo.SaveCard(old); err != nil {
			return nil, err
		}
		evicted = append(evicted, old.WordID)
	}
	target.BoostFlag = true
	target.BoostSequence = sequence + 1
	if err := s.repo.SaveCard(target); err != nil {
		return nil, err
	}
	return evicted, nil
}
