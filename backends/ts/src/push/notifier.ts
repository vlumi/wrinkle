// Push notifications, through APNs, to devices that are not connected: the
// fan-out tells the notifier after appending a message or a deletion to a
// user's log; connected devices get the event over their WebSocket instead.
import type { Pool } from "mysql2/promise";
import type { MessageDeletedEvent } from "../gen/wrinkle/v1/event_pb.js";
import type { Message } from "../gen/wrinkle/v1/model_pb.js";
import { exec, row, rows } from "../internal/db.ts";
import { hex, isZero, parseUUID, sameId, type UUID } from "../internal/ids.ts";
import { blocks } from "../contacts/sides.ts";
import type { Logger } from "../internal/log.ts";
import * as metrics from "../internal/metrics.ts";
import { maxPayload, payload, UnregisteredError, type Alert, type APNs } from "./apns.ts";

/** Which of a user's devices are connected to some gateway. */
export interface Presence {
  connectedDevices(userId: UUID): Promise<Set<bigint>>;
}

const maxInFlight = 64;
const sendTimeout = 10_000;
/** What an alert says when the extension cannot decrypt the message. */
const genericBody = "New message";
const sandboxService = "PUSH_SERVICE_APNS_SANDBOX";

interface Registration {
  deviceId: bigint;
  service: string;
  token: string;
}

/**
 * Pushes a message, or the deletion of one, to a user's devices that are
 * not connected and have registered a token. Sends run in the background,
 * bounded, so the fan-out never waits for APNs.
 */
export class Notifier {
  private readonly apns: APNs;
  private readonly db: Pool;
  private readonly presence: Presence;
  private readonly log: Logger;
  private inFlight = 0;

  constructor(apns: APNs, userDb: Pool, presence: Presence, log: Logger) {
    this.apns = apns;
    this.db = userDb;
    this.presence = presence;
    this.log = log;
  }

  /**
   * After a message was appended to the user's log. The sender's own
   * devices are not notified, nor anyone of notices, nor in a group a user
   * who blocked the sender.
   */
  messageDelivered(userId: UUID, msg: Message, group: boolean): void {
    if (sameId(Buffer.from(msg.senderUserId), userId) || msg.content.case === "notice") return;
    const senderId = group ? parseUUID(msg.senderUserId) : undefined;
    this.background(userId, (signal) => this.notify(userId, senderId, msg, signal));
  }

  /**
   * After a deletion was appended to the user's log. A background
   * notification lets the app withdraw the message's alert if it showed one;
   * none goes to a user who blocked the sender, when the sender is given.
   */
  messageDeleted(userId: UUID, deleted: MessageDeletedEvent, senderId: UUID | undefined): void {
    this.background(userId, (signal) => this.withdraw(userId, senderId, deleted, signal));
  }

  /** Runs a send in the background, when a slot is free. */
  private background(userId: UUID, send: (signal: AbortSignal) => Promise<void>): void {
    if (this.inFlight >= maxInFlight) {
      // APNs is slow or down; the device syncs when it opens anyway.
      metrics.pushSent.inc({ result: "dropped" });
      return;
    }
    this.inFlight++;
    send(AbortSignal.timeout(sendTimeout))
      .catch((err: unknown) => this.log.warn("push failed", { user: userId, err }))
      .finally(() => this.inFlight--);
  }

  /** The user's registered devices, none when the user blocked the sender, unless no sender is given. */
  private async registrationsFor(userId: UUID, senderId: UUID | undefined): Promise<Registration[]> {
    const regs = await this.registrations(userId);
    if (regs.length === 0 || !senderId || isZero(senderId)) return regs;
    return (await blocks(this.db, userId, senderId)) ? [] : regs;
  }

  private async notify(userId: UUID, senderId: UUID | undefined, msg: Message, signal: AbortSignal): Promise<void> {
    const regs = await this.registrationsFor(userId, senderId);
    if (regs.length === 0) return;
    const conversationId = parseUUID(msg.conversationId);
    if (!conversationId) throw new Error("message without a conversation id");
    const title = await this.alertTitle(userId, conversationId);
    const badge = await this.unread(userId);
    await this.sendDisconnected(userId, regs, messageAlert(title, badge, conversationId, msg), signal);
  }

  private async withdraw(
    userId: UUID,
    senderId: UUID | undefined,
    deleted: MessageDeletedEvent,
    signal: AbortSignal,
  ): Promise<void> {
    const regs = await this.registrationsFor(userId, senderId);
    if (regs.length === 0) return;
    const conversationId = parseUUID(deleted.conversationId);
    if (!conversationId) throw new Error("deletion without a conversation id");
    const alert: Alert = {
      background: true,
      data: { conversation_id: hex(conversationId), seq: Number(deleted.seq), deleted: true },
    };
    await this.sendDisconnected(userId, regs, alert, signal);
  }

  /** Sends the alert to each registered device that is not connected. */
  private async sendDisconnected(userId: UUID, regs: Registration[], alert: Alert, signal: AbortSignal): Promise<void> {
    const connected = await this.presence.connectedDevices(userId);
    for (const r of regs) {
      if (!connected.has(r.deviceId)) await this.send(userId, r, alert, signal);
    }
  }

  /** The notification's title: in a direct conversation the peer's current name, from the conversation list; in a group the topic. */
  private async alertTitle(userId: UUID, conversationId: UUID): Promise<string> {
    const entry = await row<{ kind: string; peer_name: string; topic: string }>(
      this.db,
      "SELECT kind, peer_name, topic FROM user_conversation WHERE user_id = ? AND conversation_id = ?",
      [userId, conversationId],
    );
    if (!entry) throw new Error("conversation entry: no such row");
    return entry.kind !== "group" ? entry.peer_name : entry.topic;
  }

  private async send(userId: UUID, r: Registration, alert: Alert, signal: AbortSignal): Promise<void> {
    try {
      await this.apns.send(r.service === sandboxService, r.token, alert, signal);
      metrics.pushSent.inc({ result: "ok" });
    } catch (err) {
      if (err instanceof UnregisteredError) {
        metrics.pushSent.inc({ result: "unregistered" });
        await this.unregister(userId, r).catch((e: unknown) => this.log.warn("clearing push token failed", { err: e }));
        return;
      }
      metrics.pushSent.inc({ result: "error" });
      this.log.warn("push send failed", { user: userId, device: r.deviceId, err });
    }
  }

  private async registrations(userId: UUID): Promise<Registration[]> {
    const found = await rows<{ device_id: bigint; push_service: string | null; push_token: string }>(
      this.db,
      "SELECT device_id, push_service, push_token FROM device WHERE user_id = ? AND push_token IS NOT NULL",
      [userId],
    );
    return found.map((r) => ({ deviceId: r.device_id, service: r.push_service ?? "", token: r.push_token }));
  }

  /** The user's unread messages across conversations, for the badge. */
  private async unread(userId: UUID): Promise<number> {
    const r = await row<{ unread: unknown }>(
      this.db,
      "SELECT COALESCE(SUM(last_seq - read_seq), 0) AS unread FROM user_conversation WHERE user_id = ?",
      [userId],
    );
    return Number(r?.unread ?? 0);
  }

  /** Removes a token APNs rejected, unless the device has registered a new one meanwhile. */
  private async unregister(userId: UUID, r: Registration): Promise<void> {
    await exec(
      this.db,
      `UPDATE device SET push_service = NULL, push_token = NULL
       WHERE user_id = ? AND device_id = ? AND push_token = ?`,
      [userId, r.deviceId, r.token],
    );
  }
}

/**
 * The notification of a message: the server cannot read it, so the body is
 * generic and the sealed content rides along, when it fits, for the app's
 * extension to decrypt and show instead.
 */
export function messageAlert(title: string, badge: number, conversationId: UUID, msg: Message): Alert {
  // Clients find the conversation by the lowercase hex of its id.
  const plain = { conversation_id: hex(conversationId), seq: Number(msg.seq) };
  const alert: Alert = {
    title,
    body: genericBody,
    badge,
    threadId: hex(conversationId),
    mutableContent: true,
    data: plain,
  };
  if (msg.content.case !== "sealed") return alert;
  const sealed = msg.content.value;
  alert.data = {
    ...plain,
    epoch: sealed.epoch,
    nonce: Buffer.from(sealed.nonce).toString("base64"),
    ciphertext: Buffer.from(sealed.ciphertext).toString("base64"),
    sender_user_id: hex(msg.senderUserId),
    client_message_id: msg.clientMessageId,
  };
  if (payload(alert).length > maxPayload) alert.data = plain;
  return alert;
}
