// Contact changes: a user's side toward another, to both users.
import { create } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ContactEventSchema } from "../gen/wrinkle/v1/event_pb.js";
import { ContactState } from "../gen/wrinkle/v1/model_pb.js";
import { hex, parseUUID, sameId } from "../internal/ids.ts";
import type { Delivery } from "./deliver.ts";
import type { UserOutboxRow } from "./outbox.ts";
import { contactOf, copyPeer, endRequest, payloadSide, Request, sides } from "../contacts/sides.ts";

/**
 * Handles a change of a user's side toward another: the side is copied to
 * the other's contact_peer, the other's request ends when the side ended or
 * became a block, through the other's outbox, and both users get how they
 * now stand, each view read under the lock on the log it goes to, so the
 * last event in a log is the current state. Every step may run again on
 * redelivery.
 */
export async function deliverContact(d: Delivery, o: UserOutboxRow): Promise<void> {
  const c = o.event.body.case === "contact" ? o.event.body.value.contact : undefined;
  const otherId = parseUUID(c?.user?.userId);
  if (!otherId || sameId(otherId, o.userId)) {
    d.log.error("skipping contact change without another user", { user: o.userId, outbox: o.outboxId });
    return;
  }
  const user = await d.profile(o.userId);
  const other = await d.profile(otherId);
  if (other.name !== "" && !other.deleted) {
    const at = o.event.occurredAt ? timestampDate(o.event.occurredAt) : new Date(0);
    const side = payloadSide(c?.state ?? ContactState.UNSPECIFIED);
    let current: boolean;
    try {
      current = await copyPeer(d.db, otherId, o.userId, side, o.outboxId, at);
    } catch (err) {
      throw new Error(`copy contact of user ${hex(o.userId)} to ${hex(otherId)}: ${(err as Error).message}`, {
        cause: err,
      });
    }
    // Only the latest side ends a request: a redelivered older row must not
    // end one made after it.
    if (current && side !== Request) {
      try {
        await endRequest(d.db, otherId, o.userId);
      } catch (err) {
        throw new Error(`end request of user ${hex(otherId)}: ${(err as Error).message}`, { cause: err });
      }
    }
  }
  for (const to of [
    { userId: o.userId, otherId, other },
    { userId: otherId, otherId: o.userId, other: user },
  ]) {
    try {
      await d.appendBuilt(
        to.userId,
        o.userId,
        o.outboxId,
        o.event,
        async (tx, event) => {
          const { own, peer } = await sides(tx, to.userId, to.otherId);
          event.body = {
            case: "contact",
            value: create(ContactEventSchema, { contact: contactOf(to.other, own, peer) }),
          };
        },
        undefined,
      );
    } catch (err) {
      throw new Error(`deliver to user ${hex(to.userId)}: ${(err as Error).message}`, { cause: err });
    }
  }
}
