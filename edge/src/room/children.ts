// Children: the sandboxes and browsers a room runs (A3). Filled in by A1-A3.
import type { Env } from "../env";
import { EdgeError } from "../http";
import type { Caller, RoomDO } from "./room";

export class Children {
  constructor(
    readonly room: RoomDO,
    readonly ctx: DurableObjectState,
    readonly env: Env,
  ) {}

  async route(_request: Request, _url: URL, _rest: string, _caller: Caller): Promise<Response> {
    throw new EdgeError("not_found", "no such route");
  }

  async sweep(): Promise<void> {}

  nextSweep(): number | null {
    return null;
  }
}
