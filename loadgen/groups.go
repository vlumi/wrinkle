package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// contactsTimeout bounds the wait for a group's members to become the
// creator's contacts.
const contactsTimeout = 30 * time.Second

// formGroups puts consecutive users, size at a time, in a group created by
// the first of them, before anything is measured, once they are the
// creator's contacts; users left over send directly only. The client group id names the block, so later runs find
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
			var members []*device
			for u := first + 1; u < first+size; u++ {
				if len(byUser[u]) > 0 {
					members = append(members, byUser[u][0])
				}
			}
			if err := befriend(ctx, creator[0], members, names); err != nil {
				stats.connectErrors.Add(1)
				fmt.Fprintf(os.Stderr, "contacts of %s: %v\n", creator[0].userName, err)
				return
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

// befriend makes the named users the creator's contacts: the creator asks
// each, each member asks the creator, which also accepts, then the
// creator's list is polled until all are contacts. Asking again changes
// nothing, so later runs pass through.
func befriend(ctx context.Context, creator *device, members []*device, names []string) error {
	call := func(d *device, name string) error {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		_, err := d.client.AddContact(cctx, name)
		return err
	}
	for _, name := range names {
		if err := call(creator, name); err != nil {
			return fmt.Errorf("ask %s: %w", name, err)
		}
	}
	for _, m := range members {
		if err := call(m, creator.userName); err != nil {
			return fmt.Errorf("%s accepts: %w", m.userName, err)
		}
	}
	deadline := time.Now().Add(contactsTimeout)
	for {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		list, err := creator.client.ListContacts(cctx)
		cancel()
		if err != nil {
			return err
		}
		contacts := map[string]bool{}
		for _, c := range list {
			if c.GetState() == pb.ContactState_CONTACT_STATE_CONTACT {
				contacts[c.GetUser().GetName()] = true
			}
		}
		missing := 0
		for _, name := range names {
			if !contacts[name] {
				missing++
			}
		}
		if missing == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d of %d members not contacts after %s", missing, len(names), contactsTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
