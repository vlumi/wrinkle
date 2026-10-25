package wrinkle;

import com.linecorp.armeria.common.HttpResponse;
import com.linecorp.armeria.common.HttpStatus;
import com.linecorp.armeria.server.Server;
import com.linecorp.armeria.server.ServerBuilder;
import com.linecorp.armeria.server.grpc.GrpcService;
import com.linecorp.armeria.server.prometheus.PrometheusExpositionService;
import io.prometheus.metrics.model.registry.PrometheusRegistry;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.accounts.AccountService;
import wrinkle.accounts.Devices;
import wrinkle.contacts.ContactService;
import wrinkle.contacts.Contacts;
import wrinkle.conversation.Cold;
import wrinkle.conversation.ColdMover;
import wrinkle.conversation.ConversationKeys;
import wrinkle.conversation.ConversationService;
import wrinkle.conversation.GroupService;
import wrinkle.devicesync.DeviceSyncService;
import wrinkle.devicesync.LogTrimmer;
import wrinkle.fanout.FanoutWorker;
import wrinkle.gateway.Gateway;
import wrinkle.presence.Presence;
import wrinkle.push.Apns;
import wrinkle.push.Notifier;
import wrinkle.services.Backend;
import wrinkle.services.LocalBackend;
import wrinkle.services.RpcClient;
import wrinkle.services.RpcServer;
import wrinkle.stickers.StickerService;
import wrinkle.uploads.Blobs;
import wrinkle.uploads.Presigner;
import wrinkle.uploads.UploadService;
import wrinkle.uploads.UploadSweeper;

/**
 * The Java backend: one binary whose roles are chosen by configuration. The gateway role serves
 * WebSocket connections, with the account, conversation and sync services in the same process or
 * forwarding to a services process; the services role serves the internal RPC; the fanout role
 * consumes the outbox from Kafka, deletes unused blobs, trims the user logs and moves old history
 * to the cold tier.
 */
public final class Main {
  private static final Logger log = LoggerFactory.getLogger(Main.class);
  private static final String FANOUT_GROUP = "wrinkle-fanout";
  private static final String USER_KEYSPACE = "user";
  // A drain that has not finished by then exits anyway.
  private static final long SHUTDOWN_LIMIT_MILLIS = 9500;
  // Leaves the fan-out consumer time to leave its group before the limit.
  private static final long FANOUT_STOP_MARGIN_MILLIS = 300;
  // Requests in flight when the server stops get this long to finish.
  private static final Duration GRACE_QUIET = Duration.ofMillis(200);
  private static final Duration GRACE_TIMEOUT = Duration.ofSeconds(3);

  private Main() {}

  public static void main(String[] args) throws Exception {
    Config cfg = Config.parse(args);
    if (cfg.settingsFile() != null) {
      log.info("settings read from {}", cfg.settingsFile());
    }
    Metrics.init();
    Shared shared = new Shared(cfg);

    AtomicBoolean draining = new AtomicBoolean();
    ServerBuilder sb =
        Server.builder().http(cfg.listen()).gracefulShutdownTimeout(GRACE_QUIET, GRACE_TIMEOUT);
    addOperationalEndpoints(sb, draining);

    Gateway gateway = null;
    if (cfg.runsGateway()) {
      Backend backend = cfg.forwardsToServices() ? new RpcClient(cfg.services()) : shared.local();
      gateway = new Gateway(backend, shared.presence(), cfg.ackIntervalMillis());
      gateway.register(sb, draining::get);
      if (cfg.forwardsToServices()) {
        log.info("gateway forwarding to services at {}", cfg.services());
      }
    }
    if (cfg.runsServices()) {
      // The RPC on its own port, as in the Go backend.
      sb.http(cfg.servicesListen());
      sb.virtualHost(cfg.servicesListen().getPort())
          .service(GrpcService.builder().addService(new RpcServer(shared.local())).build());
      log.info("services listening on {}", cfg.servicesListen());
    }
    FanoutWorker fanout = null;
    List<ShardWork> shardWork = new ArrayList<>();
    if (cfg.runsFanout()) {
      Notifier notifier = null;
      if (!cfg.apnsKey().isEmpty()) {
        Apns apns =
            new Apns(
                new Apns.Config(
                    cfg.apnsKey(),
                    cfg.apnsKeyId(),
                    cfg.apnsTeamId(),
                    cfg.apnsTopic(),
                    null,
                    null,
                    null));
        notifier =
            new Notifier(apns, shared.userDb(), new Contacts(shared.userDb()), shared.presence());
        log.info("push notifications on, topic {}", cfg.apnsTopic());
      }
      fanout =
          new FanoutWorker(
              cfg.kafka(),
              FANOUT_GROUP,
              shared.userDb(),
              USER_KEYSPACE,
              shared.conversationDb(),
              shared.presence(),
              notifier);
      fanout.start();
      log.info("fanout consuming {}", FanoutWorker.TOPICS);
      new UploadSweeper(shared.userDb(), shared.blobs()).start();
      // Each shard's trim and move is taken by one instance at a time.
      LogTrimmer trimmer =
          new LogTrimmer(cfg.mysql(), USER_KEYSPACE, shared.userDb(), cfg.logRetention());
      shardWork.add(
          new ShardWork(
              "trimming the user logs",
              USER_KEYSPACE,
              trimmer.shards(),
              cfg.logTrimInterval(),
              "events",
              trimmer::trimShard,
              shared.presence(),
              LoggerFactory.getLogger(LogTrimmer.class)));
      ColdMover mover =
          new ColdMover(
              cfg.mysql(),
              "conversation",
              shared.conversationDb(),
              shared.cold(),
              cfg.historyHot());
      shardWork.add(
          new ShardWork(
              "moving history to the cold tier",
              "conversation",
              mover.shards(),
              cfg.coldMoveInterval(),
              "messages",
              mover::moveShard,
              shared.presence(),
              LoggerFactory.getLogger(ColdMover.class)));
      shardWork.forEach(ShardWork::start);
    }

    Server server = sb.build();
    if (gateway != null) {
      gateway.start();
    }
    server.start().join();
    log.info("listening on {}", cfg.listen());

    Gateway g = gateway;
    FanoutWorker f = fanout;
    Runtime.getRuntime()
        .addShutdownHook(
            Thread.ofPlatform()
                .unstarted(() -> shutDown(draining, server, g, f, shardWork, shared)));
    // Until the shutdown hook halts the process.
    new CountDownLatch(1).await();
  }

  /**
   * What the roles share, opened when the first role asks: the pools of the two keyspaces, the cold
   * tier and the services on them, and Valkey. So a gateway that forwards opens no pool, and a
   * services-only process never connects to Valkey.
   */
  private static final class Shared {
    private final Config cfg;
    private final Blobs blobs;
    private DataSource userDb;
    private DataSource conversationDb;
    private Cold cold;
    private LocalBackend local;
    private Presence presence;

    Shared(Config cfg) {
      this.cfg = cfg;
      this.blobs =
          new Blobs(
              cfg.blobUrl(), new Presigner(cfg.blobKeyId(), cfg.blobSecret(), cfg.blobRegion()));
    }

    Blobs blobs() {
      return blobs;
    }

    DataSource userDb() {
      if (userDb == null) {
        userDb = Db.open(cfg.mysql(), USER_KEYSPACE, cfg.dbConns());
      }
      return userDb;
    }

    DataSource conversationDb() {
      if (conversationDb == null) {
        conversationDb = Db.open(cfg.mysql(), "conversation", cfg.dbConns());
      }
      return conversationDb;
    }

    Cold cold() {
      if (cold == null) {
        cold = new Cold(cfg.hbase());
      }
      return cold;
    }

    LocalBackend local() {
      if (local == null) {
        UploadService uploads = new UploadService(userDb(), blobs);
        Contacts contacts = new Contacts(userDb());
        ConversationService conversations =
            new ConversationService(conversationDb(), uploads, contacts, cold());
        AccountService accounts = new AccountService(userDb(), uploads);
        local =
            new LocalBackend(
                accounts,
                new Devices(userDb()),
                uploads,
                conversations,
                new GroupService(conversationDb(), userDb(), accounts, contacts, cold()),
                new ConversationKeys(conversationDb(), userDb()),
                new DeviceSyncService(userDb(), accounts, conversations, contacts),
                new StickerService(userDb(), uploads),
                new ContactService(userDb(), accounts, contacts));
      }
      return local;
    }

    Presence presence() {
      if (presence == null) {
        presence = new Presence(cfg.valkey(), cfg.node());
      }
      return presence;
    }

    void close() {
      if (presence != null) {
        presence.close();
      }
      if (cold != null) {
        cold.close();
      }
    }
  }

  private static void addOperationalEndpoints(ServerBuilder sb, AtomicBoolean draining) {
    sb.service(
        "/healthz",
        (ctx, req) ->
            draining.get()
                ? HttpResponse.of(HttpStatus.SERVICE_UNAVAILABLE)
                : HttpResponse.of("ok\n"));
    sb.service("/metrics", PrometheusExpositionService.of(PrometheusRegistry.defaultRegistry));
    // Lets a measurement collect garbage before sampling memory.
    sb.service(
        "/debug/gc",
        (ctx, req) -> {
          System.gc();
          return HttpResponse.of("ok\n");
        });
    sb.decorator(
        "prefix:/debug/",
        (delegate, ctx, req) ->
            ctx.remoteAddress().getAddress().isLoopbackAddress()
                ? delegate.serve(ctx, req)
                : HttpResponse.of(HttpStatus.FORBIDDEN));
  }

  /**
   * Drains on SIGTERM or SIGINT: refuses new work, closes the connections and lets requests in
   * flight finish, stops the roles that run, and exits 0 within the limit. The fan-out consumer
   * stops alongside the rest and is waited for, so it leaves its group before the exit; so is the
   * work per shard, so it drops its leases.
   */
  private static void shutDown(
      AtomicBoolean draining,
      Server server,
      Gateway gateway,
      FanoutWorker fanout,
      List<ShardWork> shardWork,
      Shared shared) {
    log.info("shutting down");
    long deadline = System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(SHUTDOWN_LIMIT_MILLIS);
    draining.set(true);
    Thread.ofPlatform()
        .daemon()
        .start(
            () -> {
              try {
                TimeUnit.MILLISECONDS.sleep(SHUTDOWN_LIMIT_MILLIS);
              } catch (InterruptedException e) {
                return;
              }
              log.warn("shutdown took too long");
              Runtime.getRuntime().halt(0);
            });
    try {
      if (fanout != null) {
        fanout.stop();
      }
      shardWork.forEach(ShardWork::stop);
      if (gateway != null) {
        gateway.drain();
      }
      server.stop().join();
      if (fanout != null) {
        long left =
            TimeUnit.NANOSECONDS.toMillis(deadline - System.nanoTime()) - FANOUT_STOP_MARGIN_MILLIS;
        if (!fanout.awaitStopped(left)) {
          log.warn("fanout consumer did not stop in time");
        }
      }
      for (ShardWork w : shardWork) {
        long left = TimeUnit.NANOSECONDS.toMillis(deadline - System.nanoTime());
        if (!w.awaitStopped(left)) {
          log.warn("work per shard did not stop in time");
        }
      }
      shared.close();
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
    } catch (RuntimeException e) {
      log.error("shutdown failed", e);
    }
    // A signal would otherwise make the exit status 128 plus its number.
    Runtime.getRuntime().halt(0);
  }
}
