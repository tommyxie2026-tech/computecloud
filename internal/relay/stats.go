package relay

import "sync/atomic"

type rejectionReason uint8

const (
	rejectionInvalid rejectionReason = iota + 1
	rejectionReplay
	rejectionCapacity
	rejectionQuota
	rejectionTimeout
	rejectionNotReady
)

// StatsSnapshot contains fixed-cardinality transport counters only. It never
// includes identities, ticket material, addresses, or forwarded payloads.
type StatsSnapshot struct {
	ActiveConnections uint64 `json:"active_connections"`
	AcceptedPairs     uint64 `json:"accepted_pairs"`
	BytesIn           uint64 `json:"bytes_in"`
	BytesOut          uint64 `json:"bytes_out"`
	BackpressureTotal uint64 `json:"backpressure_total"`
	RejectedInvalid   uint64 `json:"rejected_invalid"`
	RejectedReplay    uint64 `json:"rejected_replay"`
	RejectedCapacity  uint64 `json:"rejected_capacity"`
	RejectedQuota     uint64 `json:"rejected_quota"`
	RejectedTimeout   uint64 `json:"rejected_timeout"`
	RejectedNotReady  uint64 `json:"rejected_not_ready"`
}

type brokerStats struct {
	active, accepted, bytesIn, bytesOut, backpressure                  atomic.Uint64
	rejectedInvalid, rejectedReplay                                    atomic.Uint64
	rejectedCapacity, rejectedQuota, rejectedTimeout, rejectedNotReady atomic.Uint64
}

func (b *Broker) recordPairOpened() {
	b.stats.active.Add(1)
	b.stats.accepted.Add(1)
}

func (b *Broker) recordPairClosed() { b.stats.active.Add(^uint64(0)) }

func (b *Broker) recordForwarded(n int, backpressured bool) {
	if n <= 0 {
		return
	}
	b.stats.bytesIn.Add(uint64(n))
	b.stats.bytesOut.Add(uint64(n))
	if backpressured {
		b.stats.backpressure.Add(1)
	}
}

func (b *Broker) recordRejected(reason rejectionReason) {
	switch reason {
	case rejectionInvalid:
		b.stats.rejectedInvalid.Add(1)
	case rejectionReplay:
		b.stats.rejectedReplay.Add(1)
	case rejectionCapacity:
		b.stats.rejectedCapacity.Add(1)
	case rejectionQuota:
		b.stats.rejectedQuota.Add(1)
	case rejectionTimeout:
		b.stats.rejectedTimeout.Add(1)
	case rejectionNotReady:
		b.stats.rejectedNotReady.Add(1)
	}
}

func (b *Broker) Stats() StatsSnapshot {
	return StatsSnapshot{
		ActiveConnections: b.stats.active.Load(),
		AcceptedPairs:     b.stats.accepted.Load(),
		BytesIn:           b.stats.bytesIn.Load(),
		BytesOut:          b.stats.bytesOut.Load(),
		BackpressureTotal: b.stats.backpressure.Load(),
		RejectedInvalid:   b.stats.rejectedInvalid.Load(),
		RejectedReplay:    b.stats.rejectedReplay.Load(),
		RejectedCapacity:  b.stats.rejectedCapacity.Load(),
		RejectedQuota:     b.stats.rejectedQuota.Load(),
		RejectedTimeout:   b.stats.rejectedTimeout.Load(),
		RejectedNotReady:  b.stats.rejectedNotReady.Load(),
	}
}
