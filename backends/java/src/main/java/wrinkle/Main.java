package wrinkle;

import com.linecorp.armeria.common.HttpResponse;
import com.linecorp.armeria.server.Server;
import com.linecorp.armeria.server.ServerBuilder;
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

/**
 * The Java backend: one binary whose roles are chosen by configuration. The gateway role serves
 * WebSocket connections with the conversation and sync services in the same process; the fanout
 * role consumes the outbox from Kafka.
 */
public final class Main {
  private static final Logger log = LoggerFactory.getLogger(Main.class);

  private Main() {}

  public static void main(String[] args) throws Exception {
    Config cfg = Config.parse(args);
    Metrics.init();

    DataSource conversationDb = Db.open(cfg.mysql(), "conversation", cfg.dbConns());
    DataSource userDb = Db.open(cfg.mysql(), "user", cfg.dbConns());
    Presence presence = new Presence(cfg.valkey(), cfg.node());

    ServerBuilder sb = Server.builder().http(cfg.port());
    sb.service("/healthz", (ctx, req) -> HttpResponse.of("ok\n"));
    sb.service("/metrics", PrometheusExpositionService.of(PrometheusRegistry.defaultRegistry));

    Gateway gateway = null;
    if (cfg.roles().contains("gateway")) {
      ConversationService conversations = new ConversationService(conversationDb);
      DeviceSyncService users = new DeviceSyncService(userDb, conversations);
      gateway = new Gateway(conversations, users, presence);
      gateway.register(sb);
    }
    FanoutWorker fanout = null;
    if (cfg.roles().contains("fanout")) {
      fanout = new FanoutWorker(cfg.kafka(), "wrinkle-fanout", userDb, presence);
      Thread.ofPlatform().name("fanout").start(fanout::run);
      log.info("fanout consuming {}", FanoutWorker.TOPIC);
    }

    Server server = sb.build();
    server.start().join();
    log.info("listening on {}", cfg.port());

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
