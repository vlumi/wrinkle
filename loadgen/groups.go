package main

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// formGroups puts consecutive users, size at a time, in a group created by
// the first of them, before anything is measured; users left over send
// directly only. The client group id names the block, so later runs find
// the same groups. A group that fails leaves its users without one.
func formGroups(ctx context.Context, devices []*device, size, devicesPerUser int, stats *stats) {
	byUser := map[int][]*device{}
	users := 0
	for _, d := range devices {
		byUser[d.user] = append(byUser[d.user], d)
		users = max(users, d.user+1)
	}
	var wg sync.WaitGroup
	for first := 0; first+size <= users; first += size {
		creator := byUser[first]
		if len(creator) == 0 {
			continue
		}
		wg.Go(func() {
			var names []string
			for u := first + 1; u < first+size; u++ {
				names = append(names, fmt.Sprintf("lg-%d", u))
			}
			cctx, cancel := context.WithTimeout(ctx, callTimeout)
			defer cancel()
			conv, err := creator[0].client.CreateGroup(cctx, fmt.Sprintf("Load %d", first/size), names,
				fmt.Sprintf("lg-group-%d-%d", size, first/size))
			if err != nil {
				stats.connectErrors.Add(1)
				fmt.Fprintf(os.Stderr, "create group of %s: %v\n", creator[0].userName, err)
				return
			}
			for u := first; u < first+size; u++ {
				for _, d := range byUser[u] {
					d.groupID = conv.GetConversationId()
					d.groupReceivers = int64((size - 1) * devicesPerUser)
				}
			}
		})
	}
	wg.Wait()
}
