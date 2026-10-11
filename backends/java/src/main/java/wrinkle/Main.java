package wrinkle;

import com.linecorp.armeria.common.HttpResponse;
import com.linecorp.armeria.server.Server;
import com.linecorp.armeria.server.ServerBuilder;
import com.linecorp.armeria.server.grpc.GrpcService;
import com.linecorp.armeria.server.prometheus.PrometheusExpositionService;
import io.prometheus.metrics.model.registry.PrometheusRegistry;
import java.util.concurrent.CompletableFuture;
import javax.sql.DataSource;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import wrinkle.conversation.ConversationService;
import wrinkle.devicesync.DeviceSyncService;
import wrinkle.fanout.FanoutWorker;
import wrinkle.gateway.Gateway;
import wrinkle.presence.Presence;
import wrinkle.services.Backend;
import wrinkle.services.LocalBackend;
import wrinkle.services.RpcClient;
import wrinkle.services.RpcServer;

/**
 * The Java backend: one binary whose roles are chosen by configuration. The gateway role serves
 * WebSocket connections, with the conversation and sync services in the same process or forwarding
 * to a services process; the services role serves the internal RPC; the fanout role consumes the
 * outbox from Kafka.
 */
public final class Main {
  private static final Logger log = LoggerFactory.getLogger(Main.class);
  private static final String FANOUT_GROUP = "wrinkle-fanout";

  private Main() {}

  public static void main(String[] args) throws Exception {
    Config cfg = Config.parse(args);
    Metrics.init();

    DataSource userDb = null;
    LocalBackend local = null;
    if (cfg.needsDatabase()) {
      DataSource conversationDb = Db.open(cfg.mysql(), "conversation", cfg.dbConns());
      userDb = Db.open(cfg.mysql(), "user", cfg.dbConns());
      ConversationService conversations = new ConversationService(conversationDb);
      local = new LocalBackend(conversations, new DeviceSyncService(userDb, conversations));
    }
    Presence presence = new Presence(cfg.valkey(), cfg.node());

    ServerBuilder sb = Server.builder().http(cfg.listen());
    addOperationalEndpoints(sb);

    Gateway gateway = null;
    if (cfg.runsGateway()) {
      Backend backend = cfg.forwardsToServices() ? new RpcClient(cfg.services()) : local;
      gateway = new Gateway(backend, presence);
      gateway.register(sb);
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
      fanout = new FanoutWorker(cfg.kafka(), FANOUT_GROUP, userDb, presence);
      Thread.ofPlatform().name("fanout").start(fanout::run);
      log.info("fanout consuming {}", FanoutWorker.TOPIC);
    }

    Server server = sb.build();
    server.start().join();
    log.info("listening on {}", cfg.listen());

    awaitShutdown(server, gateway, fanout, presence);
  }

  private static void addOperationalEndpoints(ServerBuilder sb) {
    sb.service("/healthz", (ctx, req) -> HttpResponse.of("ok\n"));
    sb.service("/metrics", PrometheusExpositionService.of(PrometheusRegistry.defaultRegistry));
    // Lets a measurement collect garbage before sampling memory.
    sb.service(
        "/debug/gc",
        (ctx, req) -> {
          System.gc();
          return HttpResponse.of("ok\n");
        });
  }

  /** Blocks until the process is told to stop, then stops the roles that run. */
  private static void awaitShutdown(
      Server server, Gateway gateway, FanoutWorker fanout, Presence presence) {
    CompletableFuture<Void> stopped = new CompletableFuture<>();
    Runnable shutDown =
        () -> {
          log.info("shutting down");
          if (fanout != null) {
            fanout.stop();
          }
          server.stop().join();
          if (gateway != null) {
            gateway.close();
          }
          presence.close();
          stopped.complete(null);
        };
    Runtime.getRuntime().addShutdownHook(Thread.ofPlatform().unstarted(shutDown));
    stopped.join();
  }
}
