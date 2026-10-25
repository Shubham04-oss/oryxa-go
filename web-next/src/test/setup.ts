// Shared test settings. The suite has grown past 2,400 tests, and on a busy
// CI runner a `findBy` or `waitFor` that needs a few query round trips can
// pass one second, failing a different test on each run. Give async lookups
// three seconds and each test fifteen, so only a real hang fails.
import { configure } from "@testing-library/dom"
import { afterEach } from "vitest"
import { forgetCallRefreshes } from "@/lib/call/use-room-call"
import { forgetRoomLogs } from "@/lib/live/use-room-log"

configure({ asyncUtilTimeout: 3_000 })

// A page shares one subscription per room and keeps it a while after the
// last hook lets go (lib/live/use-room-log): each test starts without one.
afterEach(() => {
  forgetRoomLogs()
  forgetCallRefreshes()
})
