import type { Env as EdgeEnv } from "../src/env";

declare global {
  namespace Cloudflare {
    interface Env extends EdgeEnv {
      TEST_SIGNING_KEY: string;
    }
  }
}
