// VoiceDO: filled in by its work package. Named under its room (contract §1).
import { DurableObject } from "cloudflare:workers";
import type { Env } from "../env";

export class VoiceDO extends DurableObject<Env> {}
