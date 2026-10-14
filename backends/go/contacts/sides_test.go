package contacts

import (
	"testing"
	"time"

	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

func TestView(t *testing.T) {
	for _, c := range []struct {
		own, peer string
		want      pb.ContactState
	}{
		{Block, Request, pb.ContactState_CONTACT_STATE_BLOCKED},
		{Block, "", pb.ContactState_CONTACT_STATE_BLOCKED},
		{Request, Request, pb.ContactState_CONTACT_STATE_CONTACT},
		{Request, "", pb.ContactState_CONTACT_STATE_OUTGOING},
		{Request, None, pb.ContactState_CONTACT_STATE_OUTGOING},
		{Request, Block, pb.ContactState_CONTACT_STATE_OUTGOING},
		{"", Request, pb.ContactState_CONTACT_STATE_INCOMING},
		{"", Block, pb.ContactState_CONTACT_STATE_UNSPECIFIED},
		{"", None, pb.ContactState_CONTACT_STATE_UNSPECIFIED},
		{"", "", pb.ContactState_CONTACT_STATE_UNSPECIFIED},
	} {
		if got := View(c.own, c.peer); got != c.want {
			t.Errorf("View(%q, %q) = %v, want %v", c.own, c.peer, got, c.want)
		}
	}
}

func TestPayloadSideRoundTrips(t *testing.T) {
	for _, side := range []string{Request, Block, None} {
		if got := PayloadSide(payloadState(side)); got != side {
			t.Errorf("side %q came back as %q", side, got)
		}
	}
	if got := payloadState(""); got != pb.ContactState_CONTACT_STATE_UNSPECIFIED {
		t.Errorf("no side is %v", got)
	}
}

func TestContactTakesTheLaterChange(t *testing.T) {
	early := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Minute)
	c := Contact(&pb.User{Name: "bob"}, Side{Request, early}, Side{Request, late})
	if c.GetState() != pb.ContactState_CONTACT_STATE_CONTACT || !c.GetUpdatedAt().AsTime().Equal(late) {
		t.Fatalf("contact %v", c)
	}
	if c := Contact(&pb.User{}, Side{}, Side{}); c.GetUpdatedAt() != nil {
		t.Fatalf("no rows, yet updated at %v", c.GetUpdatedAt())
	}
}
