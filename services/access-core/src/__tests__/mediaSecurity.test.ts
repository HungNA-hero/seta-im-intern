import Fastify, { type FastifyReply, type FastifyRequest } from "fastify";
import { describe, expect, test, vi } from "vitest";
import { validateMediaTransportConfig } from "../config";
import { logRequestCompletion } from "../observability/requestLogging";
import { mediaTransportSecurityHook } from "../server";

describe("media transport security", () => {
  test("request completion logs never copy unmatched filenames, signed queries, headers, or bodies", () => {
    const info = vi.fn();
    const request = {
      correlation: {
        traceId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        requestId: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        startedAt: Date.now(),
      },
      log: { info },
      method: "POST",
      routeOptions: {},
      url: "/private/secret-holiday-photo.jpg?X-Amz-Signature=signed-secret",
      headers: { authorization: "Bearer header-secret" },
      body: { filename: "secret-holiday-photo.jpg", token: "body-secret" },
    } as unknown as FastifyRequest;

    logRequestCompletion(request, { statusCode: 404 } as FastifyReply, false);

    expect(info).toHaveBeenCalledOnce();
    const logged = JSON.stringify(info.mock.calls[0]);
    for (const secret of [
      "secret-holiday-photo",
      "X-Amz-Signature",
      "signed-secret",
      "Bearer",
      "header-secret",
      "body-secret",
    ]) {
      expect(logged).not.toContain(secret);
    }
    expect(info.mock.calls[0][0]).toMatchObject({
      operation: "unmatched",
      http: { route: "unmatched" },
    });
  });

  test("an untrusted forwarded-protocol header is rejected before body parsing", async () => {
    const app = Fastify({ trustProxy: ["127.0.0.1"] });
    const parser = vi.fn((_request, body, done) => done(null, body));
    const handler = vi.fn(async () => ({ ok: true }));
    app.addContentTypeParser("application/x-media-secret", { parseAs: "string" }, parser);
    app.addHook(
      "onRequest",
      mediaTransportSecurityHook({
        requireHTTPS: true,
        allowedOrigins: ["https://app.example.test"],
        trustedProxies: ["127.0.0.1"],
      }),
    );
    app.post("/api/v1/assets/:assetId/media/uploads", handler);
    await app.ready();

    const response = await app.inject({
      method: "POST",
      url: "/api/v1/assets/33333333-3333-4333-8333-333333333333/media/uploads",
      remoteAddress: "203.0.113.9",
      headers: {
        "x-forwarded-proto": "https",
        "content-type": "application/x-media-secret",
      },
      payload: "secret-body-that-must-not-be-parsed",
    });

    expect(response.statusCode).toBe(400);
    expect(parser).not.toHaveBeenCalled();
    expect(handler).not.toHaveBeenCalled();
    expect(response.json()).toMatchObject({ error: { code: "BAD_REQUEST" } });
  });

  test("trusted proxy HTTPS receives HSTS while development may explicitly allow HTTP", async () => {
    const secure = Fastify({ trustProxy: ["127.0.0.1"] });
    secure.addHook(
      "onRequest",
      mediaTransportSecurityHook({
        requireHTTPS: true,
        allowedOrigins: ["https://app.example.test"],
        trustedProxies: ["127.0.0.1"],
      }),
    );
    secure.get("/api/v1/assets/:assetId/media/status", async () => ({ ok: true }));
    await secure.ready();

    const secureResponse = await secure.inject({
      method: "GET",
      url: "/api/v1/assets/33333333-3333-4333-8333-333333333333/media/status",
      remoteAddress: "127.0.0.1",
      headers: { "x-forwarded-proto": "https" },
    });
    expect(secureResponse.statusCode).toBe(200);
    expect(secureResponse.headers["strict-transport-security"]).toBe("max-age=31536000; includeSubDomains");

    const development = Fastify();
    development.addHook(
      "onRequest",
      mediaTransportSecurityHook({ requireHTTPS: false, allowedOrigins: ["http://localhost:3000"] }),
    );
    development.get("/api/v1/assets/:assetId/media/status", async () => ({ ok: true }));
    await development.ready();
    expect(
      (
        await development.inject({
          method: "GET",
          url: "/api/v1/assets/33333333-3333-4333-8333-333333333333/media/status",
        })
      ).statusCode,
    ).toBe(200);

    await secure.close();
    await development.close();
  });

  test("CORS preflight allows only configured media origins and request headers", async () => {
    const app = Fastify();
    app.addHook(
      "onRequest",
      mediaTransportSecurityHook({
        requireHTTPS: false,
        allowedOrigins: ["https://app.example.test"],
      }),
    );
    await app.ready();

    const allowed = await app.inject({
      method: "OPTIONS",
      url: "/api/v1/assets/33333333-3333-4333-8333-333333333333/media/uploads",
      headers: {
        origin: "https://app.example.test",
        "access-control-request-method": "POST",
        "access-control-request-headers": "content-type, idempotency-key, x-user-id, x-org-id",
      },
    });
    expect(allowed.statusCode).toBe(204);
    expect(allowed.headers["access-control-allow-origin"]).toBe("https://app.example.test");
    expect(allowed.headers.vary).toContain("Origin");

    const disallowed = await app.inject({
      method: "OPTIONS",
      url: "/api/v1/assets/33333333-3333-4333-8333-333333333333/media/uploads",
      headers: {
        origin: "https://attacker.example.test",
        "access-control-request-method": "POST",
        "access-control-request-headers": "authorization",
      },
    });
    expect(disallowed.statusCode).toBe(403);
    expect(disallowed.headers["access-control-allow-origin"]).toBeUndefined();
    expect(disallowed.json()).toMatchObject({ error: { code: "FORBIDDEN" } });

    await app.close();
  });

  test("a disallowed origin is rejected before a media request body is parsed", async () => {
    const app = Fastify();
    const parser = vi.fn((_request, body, done) => done(null, body));
    const handler = vi.fn(async () => ({ ok: true }));
    app.addContentTypeParser("application/x-media-secret", { parseAs: "string" }, parser);
    app.addHook(
      "onRequest",
      mediaTransportSecurityHook({
        requireHTTPS: false,
        allowedOrigins: ["https://app.example.test"],
      }),
    );
    app.post("/api/v1/assets/:assetId/media/uploads", handler);
    await app.ready();

    const response = await app.inject({
      method: "POST",
      url: "/api/v1/assets/33333333-3333-4333-8333-333333333333/media/uploads",
      headers: {
        origin: "https://attacker.example.test",
        "content-type": "application/x-media-secret",
      },
      payload: "secret-body-that-must-not-be-parsed",
    });
    expect(response.statusCode).toBe(403);
    expect(parser).not.toHaveBeenCalled();
    expect(handler).not.toHaveBeenCalled();

    await app.close();
  });

  test("production configuration cannot disable media HTTPS", () => {
    expect(() => validateMediaTransportConfig("production", false)).toThrow(/HTTPS/i);
    expect(() => validateMediaTransportConfig("development", false)).not.toThrow();
  });
});
