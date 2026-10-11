package server

import "sort"

type workerLoad struct {
	peer   *session
	active int
}

// chooseLeastLoadedWorker selects from the minimum-load workers in stable ID
// order, advancing after the last successfully selected worker when possible.
func chooseLeastLoadedWorker(loads []workerLoad, cursor string) (*session, string) {
	if len(loads) == 0 {
		return nil, cursor
	}
	min := loads[0].active
	for _, item := range loads[1:] {
		if item.active < min {
			min = item.active
		}
	}
	eligible := make([]workerLoad, 0, len(loads))
	for _, item := range loads {
		if item.peer != nil && item.peer.hello != nil && item.active == min {
			eligible = append(eligible, item)
		}
	}
	if len(eligible) == 0 {
		return nil, cursor
	}
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].peer.hello.WorkerId < eligible[j].peer.hello.WorkerId
	})
	selected := 0
	if cursor != "" {
		for i, item := range eligible {
			if item.peer.hello.WorkerId > cursor {
				selected = i
				break
			}
			if i == len(eligible)-1 {
				selected = 0
			}
		}
	}
	peer := eligible[selected].peer
	return peer, peer.hello.WorkerId
}
