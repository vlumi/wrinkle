package wrinkle.fanout;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;
import wrinkle.Uuid;
import wrinkle.fanout.UserLog.Membership;
import wrinkle.v1.AdminChanged;
import wrinkle.v1.GroupCreated;
import wrinkle.v1.MemberLeft;
import wrinkle.v1.MemberRemoved;
import wrinkle.v1.MembersAdded;
import wrinkle.v1.Message;
import wrinkle.v1.Notice;
import wrinkle.v1.Sealed;
import wrinkle.v1.User;

// The membership flags each notice sets in a recipient's list entry.
class MembershipTest {
  private final Uuid alice = Uuid.random();
  private final Uuid bob = Uuid.random();
  private final Uuid carol = Uuid.random();

  private static User user(Uuid id) {
    return User.newBuilder().setUserId(id.toByteString()).build();
  }

  private Message from(Uuid sender, Notice.Builder notice) {
    return Message.newBuilder().setSenderUserId(sender.toByteString()).setNotice(notice).build();
  }

  @Test
  void sealedChangesNothing() {
    Message m =
        Message.newBuilder()
            .setSenderUserId(alice.toByteString())
            .setSealed(Sealed.getDefaultInstance())
            .build();
    assertEquals(Membership.UNCHANGED, Membership.of(alice, m));
  }

  @Test
  void creatorIsAdmin() {
    Message m =
        from(alice, Notice.newBuilder().setGroupCreated(GroupCreated.newBuilder().setTopic("t")));
    assertEquals(new Membership(true, true), Membership.of(alice, m));
    assertEquals(new Membership(true, false), Membership.of(bob, m));
  }

  @Test
  void addedAndRemoved() {
    Message added =
        from(
            alice,
            Notice.newBuilder().setMembersAdded(MembersAdded.newBuilder().addUsers(user(bob))));
    assertEquals(new Membership(true, false), Membership.of(bob, added));
    assertEquals(Membership.UNCHANGED, Membership.of(carol, added));
    Message removed =
        from(
            alice,
            Notice.newBuilder().setMemberRemoved(MemberRemoved.newBuilder().setUser(user(bob))));
    assertEquals(new Membership(false, false), Membership.of(bob, removed));
    assertEquals(Membership.UNCHANGED, Membership.of(alice, removed));
  }

  @Test
  void leavingPromotes() {
    Message left =
        from(
            alice,
            Notice.newBuilder().setMemberLeft(MemberLeft.newBuilder().setPromoted(user(bob))));
    assertEquals(new Membership(false, false), Membership.of(alice, left));
    assertEquals(new Membership(null, true), Membership.of(bob, left));
    assertEquals(Membership.UNCHANGED, Membership.of(carol, left));
  }

  @Test
  void adminChanged() {
    Message m =
        from(
            alice,
            Notice.newBuilder()
                .setAdminChanged(AdminChanged.newBuilder().setUser(user(carol)).setAdmin(true)));
    assertEquals(new Membership(null, true), Membership.of(carol, m));
    assertEquals(Membership.UNCHANGED, Membership.of(bob, m));
  }
}
