// Cedar policies: an organization admin's rules narrowing who may read or
// search a resource. A policy only ever forbids; it can never grant access
// beyond what a resource's own scope and access controls already allow.
// Managing policies is always an organization-admin action, whatever
// resource they're attached to.
//
// Today the only resource with policies is a knowledge base (a rag corpus);
// the Go handler is `POST/GET /v1/organizations/{org}/corpora/{corpus}/policies`
// and `DELETE .../policies/{policyId}`. Other resource types will get their
// own path builders here later; `components/policy-editor.tsx` stays generic
// by taking whichever of these functions apply as props, rather than calling
// them directly.
import { ApiError, apiRequest } from "@/lib/api/client"

export type Policy = {
  id: string
  name: string
  /** The Cedar policy text; the service accepts only `forbid` statements. */
  text: string
  /** The user id who created it. */
  created_by: string
  created_at: string
  updated_at: string
}

export type PolicyInput = {
  name: string
  text: string
}

export type ReadOptions = { signal?: AbortSignal }

function corpusPoliciesPath(
  organizationId: string,
  corpusId: string,
  ...rest: string[]
) {
  return `/v1/organizations/${[
    organizationId,
    "corpora",
    corpusId,
    "policies",
    ...rest,
  ]
    .map(encodeURIComponent)
    .join("/")}`
}

/** A knowledge base's policies. */
export async function listCorpusPolicies(
  organizationId: string,
  corpusId: string,
  { signal }: ReadOptions = {}
): Promise<Policy[]> {
  const { policies } = await apiRequest<{ policies: Policy[] }>(
    corpusPoliciesPath(organizationId, corpusId),
    { signal }
  )
  return policies
}

/** 400 when Cedar refuses the text (not a `forbid`, or a parse error); 409 for a duplicate name. */
export async function createCorpusPolicy(
  organizationId: string,
  corpusId: string,
  input: PolicyInput
): Promise<Policy> {
  const { policy } = await apiRequest<{ policy: Policy }>(
    corpusPoliciesPath(organizationId, corpusId),
    { body: { name: input.name, text: input.text } }
  )
  return policy
}

export async function deleteCorpusPolicy(
  organizationId: string,
  corpusId: string,
  policyId: string
): Promise<void> {
  await apiRequest(corpusPoliciesPath(organizationId, corpusId, policyId), {
    method: "DELETE",
  })
}

function sentence(message: string): string {
  const trimmed = message.trim()
  const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1)
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`
}

/**
 * Copy for a failed policies call, mirroring `ragErrorMessage`. A validator
 * or Cedar parse error (400) names the problem, so it is shown as rag's own
 * messages are; 403 and 409 get fixed copy, since managing policies is
 * always an organization-admin action and a name collision always has the
 * same fix; 404 takes an optional caller-supplied `notFound`, since this
 * client doesn't know what kind of resource it's fetching policies for.
 */
export function policyErrorMessage(
  error: unknown,
  fallback: string,
  { notFound }: { notFound?: string } = {}
): string {
  if (!(error instanceof ApiError) || error.status >= 500) return fallback
  switch (error.status) {
    case 401:
      return "Your session has ended. Sign in again to continue."
    case 403:
      return "Only organization admins can manage policies."
    case 404:
      return notFound ?? fallback
    case 409:
      return "A policy with this name already exists."
  }
  const message = error.message.trim()
  if (!message || message === "Request failed") return fallback
  return sentence(message)
}
