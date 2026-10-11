package wrinkle;

import com.linecorp.armeria.common.HttpResponse;
import com.linecorp.armeria.common.HttpStatus;
import com.linecorp.armeria.server.Server;
import com.linecorp.armeria.server.ServerBuilder;
import com.linecorp.armeria.server.grpc.GrpcService;
import com.linecorp.armeria.server.prometheus.PrometheusExpositionService;
import io.prometheus.metrics.model.registry.PrometheusRegistry;
import java.time.Duration;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.accounts.AccountService;
import wrinkle.accounts.Devices;
import wrinkle.conversation.ConversationKeys;
import wrinkle.conversation.ConversationService;
import wrinkle.conversation.GroupService;
import wrinkle.devicesync.DeviceSyncService;
import wrinkle.fanout.FanoutWorker;
import wrinkle.gateway.Gateway;
import wrinkle.presence.Presence;
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
 * consumes the outbox from Kafka.
 */
public final class Main {
  private static final Logger log = LoggerFactory.getLogger(Main.class);
  private static final String FANOUT_GROUP = "wrinkle-fanout";
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
    Metrics.init();

    DataSource userDb = null;
    DataSource conversationDb = null;
    LocalBackend local = null;
    Blobs blobs =
        new Blobs(
            cfg.blobUrl(), new Presigner(cfg.blobKeyId(), cfg.blobSecret(), cfg.blobRegion()));
    if (cfg.needsDatabase()) {
      conversationDb = Db.open(cfg.mysql(), "conversation", cfg.dbConns());
      userDb = Db.open(cfg.mysql(), "user", cfg.dbConns());
      ConversationService conversations = new ConversationService(conversationDb);
      UploadService uploads = new UploadService(userDb, blobs);
      AccountService accounts = new AccountService(userDb, uploads);
      local =
          new LocalBackend(
              accounts,
              new Devices(userDb),
              uploads,
              conversations,
              new GroupService(conversationDb, userDb, accounts),
              new ConversationKeys(conversationDb, userDb),
              new DeviceSyncService(userDb, accounts, conversations),
              new StickerService(userDb, uploads));
    }
    Presence presence = cfg.needsValkey() ? new Presence(cfg.valkey(), cfg.node()) : null;

    AtomicBoolean draining = new AtomicBoolean();
    ServerBuilder sb =
        Server.builder().http(cfg.listen()).gracefulShutdownTimeout(GRACE_QUIET, GRACE_TIMEOUT);
    addOperationalEndpoints(sb, draining);

    Gateway gateway = null;
    if (cfg.runsGateway()) {
      Backend backend = cfg.forwardsToServices() ? new RpcClient(cfg.services()) : local;
      gateway = new Gateway(backend, presence);
      gateway.register(sb, draining::get);
      if (cfg.forwardsToServices()) {
        log.info("gateway forwarding to services at {}", cfg.services());
      }
    }
    if (cfg.runsServices()) {
      // The RPC on its own port, as in the Go backend.
      sb.http(cfg.servicesListen());
      sb.virtualHost(cfg.servicesListen().getPort())
          .service(GrpcService.builder().addService(new RpcServer(local)).build());
      log.info("services listening on {}", cfg.servicesListen());
    }
    FanoutWorker fanout = null;
    if (cfg.runsFanout()) {
      fanout = new FanoutWorker(cfg.kafka(), FANOUT_GROUP, userDb, conversationDb, presence);
      fanout.start();
      log.info("fanout consuming {}", FanoutWorker.TOPICS);
      new UploadSweeper(userDb, blobs).start();
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
            Thread.ofPlatform().unstarted(() -> shutDown(draining, server, g, f, presence)));
    // Until the shutdown hook halts the process.
    new CountDownLatch(1).await();
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
   * stops alongside the rest and is waited for, so it leaves its group before the exit.
   */
  private static void shutDown(
      AtomicBoolean draining,
      Server server,
      Gateway gateway,
      FanoutWorker fanout,
      Presence presence) {
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
      if (presence != null) {
        presence.close();
      }
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
    } catch (RuntimeException e) {
      log.error("shutdown failed", e);
    }
    // A signal would otherwise make the exit status 128 plus its number.
    Runtime.getRuntime().halt(0);
  }
}
