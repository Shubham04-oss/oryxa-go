import { readFileSync } from "node:fs"
import ts from "typescript"
import { describe, expect, it } from "vitest"

// wrangler does not inherit Durable Object bindings or migrations into an
// environment, so wrangler.jsonc repeats them under env.production. A class
// bound or migrated at the top level alone would work in `vite dev` and be
// missing from the Worker app.oryxa.in serves.
function readJsonc(path: string) {
  const text = readFileSync(new URL(path, import.meta.url), "utf8")
  const { config, error } = ts.parseConfigFileTextToJson(path, text)
  if (error) throw new Error(`${path}: ${String(error.messageText)}`)
  return config
}

const wrangler = readJsonc("../wrangler.jsonc")
const production = wrangler.env.production

describe("wrangler.jsonc", () => {
  it("gives production every Durable Object binding and migration", () => {
    expect(production.durable_objects).toEqual(wrangler.durable_objects)
    expect(production.migrations).toEqual(wrangler.migrations)
  })

  it("names the Worker OpenTofu routes app.oryxa.in to", () => {
    const routed = readJsonc("../../deploy/production/web.auto.tfvars.json")
    expect(production.name).toBe(routed.web_worker_name)
  })
})
