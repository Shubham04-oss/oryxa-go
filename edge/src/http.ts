// Responses and request parsing shared by every route. Every failure leaves
// as the contract's ErrorBody, with the status ERROR_STATUS gives its code.
import type { z } from "zod";
import { ERROR_STATUS, type ErrorBody, type ErrorCode } from "./contracts";

// An EdgeError thrown anywhere in a handler becomes its ErrorBody.
export class EdgeError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
    readonly retryable = false,
  ) {
    super(message);
  }
}

export function json(body: unknown, status = 200, headers?: HeadersInit): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", "cache-control": "no-store", ...headers },
  });
}

export function errorResponse(code: ErrorCode, message: string, retryable = false): Response {
  const body: ErrorBody = { error: { code, message, ...(retryable ? { retryable } : {}) } };
  return json(body, ERROR_STATUS[code]);
}

// toResponse turns whatever a handler threw into an error response, hiding
// unexpected errors' details from the caller.
export function toResponse(error: unknown): Response {
  if (error instanceof EdgeError) return errorResponse(error.code, error.message, error.retryable);
  console.error(JSON.stringify({ msg: "edge handler failed", error: String(error), stack: (error as Error)?.stack }));
  return errorResponse("internal", "internal error");
}

// MAX_BODY_BYTES bounds a JSON body: 20 files of 5 MiB, base64, fit.
export const MAX_BODY_BYTES = 160 << 20;

export async function readJson<T extends z.ZodType>(request: Request, schema: T, maxBytes = MAX_BODY_BYTES): Promise<z.infer<T>> {
  const length = Number(request.headers.get("content-length") ?? "0");
  if (length > maxBytes) throw new EdgeError("too_large", `body over ${maxBytes} bytes`);
  const text = await request.text();
  if (text.length > maxBytes) throw new EdgeError("too_large", `body over ${maxBytes} bytes`);
  let raw: unknown;
  try {
    raw = text === "" ? {} : JSON.parse(text);
  } catch {
    throw new EdgeError("invalid_request", "body is not JSON");
  }
  const parsed = schema.safeParse(raw);
  if (!parsed.success) {
    const issue = parsed.error.issues[0];
    throw new EdgeError("invalid_request", `${issue?.path.join(".") || "body"}: ${issue?.message ?? "invalid"}`);
  }
  return parsed.data;
}

export function base64Encode(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let index = 0; index < bytes.length; index += chunk) {
    binary += String.fromCharCode(...bytes.subarray(index, index + chunk));
  }
  return btoa(binary);
}

export function base64Decode(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

export function base64UrlDecode(value: string): Uint8Array {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((value.length + 3) % 4);
  return base64Decode(padded);
}

export function base64UrlEncode(bytes: Uint8Array): string {
  return base64Encode(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
