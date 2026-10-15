// Shared test settings. The suite has grown past 2,400 tests, and on a busy
// CI runner a `findBy` or `waitFor` that needs a few query round trips can
// pass one second, failing a different test on each run. Give async lookups
// three seconds and each test fifteen, so only a real hang fails.
import { configure } from "@testing-library/dom"

configure({ asyncUtilTimeout: 3_000 })
