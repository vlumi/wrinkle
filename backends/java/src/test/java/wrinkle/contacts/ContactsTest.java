package wrinkle.contacts;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;
import wrinkle.v1.ContactState;

class ContactsTest {
  @Test
  void ownBlockShows() {
    assertEquals(ContactState.CONTACT_STATE_BLOCKED, Contacts.view("block", null));
    assertEquals(ContactState.CONTACT_STATE_BLOCKED, Contacts.view("block", "request"));
  }

  @Test
  void bothAskedIsContact() {
    assertEquals(ContactState.CONTACT_STATE_CONTACT, Contacts.view("request", "request"));
  }

  @Test
  void oneSideAsked() {
    assertEquals(ContactState.CONTACT_STATE_OUTGOING, Contacts.view("request", null));
    assertEquals(ContactState.CONTACT_STATE_OUTGOING, Contacts.view("request", "none"));
    assertEquals(ContactState.CONTACT_STATE_INCOMING, Contacts.view(null, "request"));
  }

  @Test
  void peerBlockIsNeverRevealed() {
    assertEquals(ContactState.CONTACT_STATE_UNSPECIFIED, Contacts.view(null, "block"));
    assertEquals(ContactState.CONTACT_STATE_OUTGOING, Contacts.view("request", "block"));
    assertEquals(ContactState.CONTACT_STATE_UNSPECIFIED, Contacts.view(null, "none"));
    assertEquals(ContactState.CONTACT_STATE_UNSPECIFIED, Contacts.view(null, null));
  }

  @Test
  void payloadStateNamesOwnSide() {
    assertEquals("request", Contacts.side(ContactState.CONTACT_STATE_OUTGOING));
    assertEquals("block", Contacts.side(ContactState.CONTACT_STATE_BLOCKED));
    assertEquals("none", Contacts.side(ContactState.CONTACT_STATE_UNSPECIFIED));
  }
}
