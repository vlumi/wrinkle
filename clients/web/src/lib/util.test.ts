import { defaultGatewayURL, newMessageId } from "./util";

test("the gateway defaults to the page's host", () => {
  expect(defaultGatewayURL({ protocol: "http:", hostname: "mac-mini.local" })).toBe("ws://mac-mini.local:8180/v1/ws");
  expect(defaultGatewayURL({ protocol: "https:", hostname: "example.test" })).toBe("wss://example.test:8180/v1/ws");
});

test("message ids are 32 hex characters and differ", () => {
  const a = newMessageId();
  expect(a).toMatch(/^[0-9a-f]{32}$/);
  expect(newMessageId()).not.toBe(a);
});
