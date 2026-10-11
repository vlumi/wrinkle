import { blobAddress, blobURL } from "./blobs";

test("the blob address is the TLS endpoint itself, or port 9200 beside a plain one", () => {
  expect(blobAddress("wss://example.test:8443/v1/ws")).toBe("https://example.test:8443");
  expect(blobAddress("wss://example.test/v1/ws")).toBe("https://example.test");
  expect(blobAddress("ws://127.0.0.1:8080/v1/ws")).toBe("http://127.0.0.1:9200");
  expect(blobAddress("ws://localhost:8181/v1/ws")).toBe("http://localhost:9200");
  expect(blobAddress("ws://[::1]:8080/v1/ws")).toBe("http://[::1]:9200");
  expect(blobAddress("http://example.test/")).toBeNull();
  expect(blobAddress("nonsense")).toBeNull();
});

test("blobs are read by their id in lowercase hex", () => {
  const id = Uint8Array.from([0x00, 0x11, 0xab, 0xff]);
  expect(blobURL("http://localhost:9200", id)).toBe("http://localhost:9200/blobs/0011abff");
});
