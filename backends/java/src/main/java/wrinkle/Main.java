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
import wrinkle.services.Local;
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

  private Main() {}

  public static void main(String[] args) throws Exception {
    Config cfg = Config.parse(args);
    Metrics.init();

    boolean wantGateway = cfg.roles().contains("gateway");
    boolean wantServices = cfg.roles().contains("services");
    boolean forwarding = wantGateway && !cfg.services().isEmpty();

    // The database is needed by the services (in-process or as a role) and by fan-out; a gateway
    // forwarding to a services process does without.
    DataSource conversationDb = null;
    DataSource userDb = null;
    Local local = null;
    if (wantServices || cfg.roles().contains("fanout") || (wantGateway && !forwarding)) {
      conversationDb = Db.open(cfg.mysql(), "conversation", cfg.dbConns());
      userDb = Db.open(cfg.mysql(), "user", cfg.dbConns());
      ConversationService conversations = new ConversationService(conversationDb);
      local = new Local(conversations, new DeviceSyncService(userDb, conversations));
    }
    Presence presence = new Presence(cfg.valkey(), cfg.node());

    ServerBuilder sb = Server.builder().http(cfg.listen());
    sb.service("/healthz", (ctx, req) -> HttpResponse.of("ok\n"));
    sb.service("/metrics", PrometheusExpositionService.of(PrometheusRegistry.defaultRegistry));
    // Lets a measurement collect garbage before sampling memory.
    sb.service(
        "/debug/gc",
        (ctx, req) -> {
          System.gc();
          return HttpResponse.of("ok\n");
        });

    Gateway gateway = null;
    if (wantGateway) {
      Backend backend = forwarding ? new RpcClient(cfg.services()) : local;
      gateway = new Gateway(backend, presence);
      gateway.register(sb);
      if (forwarding) {
        log.info("gateway forwarding to services at {}", cfg.services());
      }
    }
    if (wantServices) {
      // The RPC on its own port, as in the Go backend.
      sb.http(cfg.servicesListen());
      sb.virtualHost(cfg.servicesListen().getPort())
          .service(GrpcService.builder().addService(new RpcServer(local)).build());
      log.info("services listening on {}", cfg.servicesListen());
    }
    FanoutWorker fanout = null;
    if (cfg.roles().contains("fanout")) {
      fanout = new FanoutWorker(cfg.kafka(), "wrinkle-fanout", userDb, presence);
      Thread.ofPlatform().name("fanout").start(fanout::run);
      log.info("fanout consuming {}", FanoutWorker.TOPIC);
    }

    Server server = sb.build();
    server.start().join();
    log.info("listening on {}", cfg.listen());

    FanoutWorker finalFanout = fanout;
    Gateway finalGateway = gateway;
    CompletableFuture<Void> stopped = new CompletableFuture<>();
    Runtime.getRuntime()
        .addShutdownHook(
            Thread.ofPlatform()
                .unstarted(
                    () -> {
                      log.info("shutting down");
                      if (finalFanout != null) {
                        finalFanout.stop();
                      }
                      server.stop().join();
                      if (finalGateway != null) {
                        finalGateway.close();
                      }
                      presence.close();
                      stopped.complete(null);
                    }));
    stopped.join();
  }
}
