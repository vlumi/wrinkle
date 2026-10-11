import { defaultGatewayURL, gatewayLabel, withGateway } from "./gateways";

test("the gateway defaults to the page's host", () => {
  expect(defaultGatewayURL({ protocol: "http:", hostname: "mac-mini.local" })).toBe("ws://mac-mini.local:8080/v1/ws");
  expect(defaultGatewayURL({ protocol: "https:", hostname: "example.test" })).toBe("wss://example.test:8080/v1/ws");
});

test("a gateway moves to the front without duplicates", () => {
  expect(withGateway([], "ws://a:8180/v1/ws")).toEqual(["ws://a:8180/v1/ws"]);
  expect(withGateway(["ws://a:8180/v1/ws", "ws://b:8181/v1/ws"], "ws://b:8181/v1/ws")).toEqual([
    "ws://b:8181/v1/ws",
    "ws://a:8180/v1/ws",
  ]);
});

test("the list keeps the most recent dozen", () => {
  let list: string[] = [];
  for (let i = 0; i < 20; i++) list = withGateway(list, `ws://h:${8000 + i}/v1/ws`);
  expect(list).toHaveLength(12);
  expect(list[0]).toBe("ws://h:8019/v1/ws");
});

test("labels are host and port", () => {
  expect(gatewayLabel("ws://mac-mini.local:8181/v1/ws")).toBe("mac-mini.local:8181");
  expect(gatewayLabel("wss://example.test:8180/v1/ws")).toBe("wss://example.test:8180");
  expect(gatewayLabel("ws://h:1/other")).toBe("h:1/other");
  expect(gatewayLabel("nonsense")).toBe("nonsense");
});
