import { useQueries } from "@tanstack/react-query";
import { catalogToolkitQueryOptions, type Toolkit } from "@/lib/apps/client";

/**
 * Each of `toolkits`' catalog entry (name, logo, description), fetched once
 * per slug and cached. A slug still loading, or one the catalog does not
 * know, is simply missing from the map.
 */
export function useToolkitCatalog(
	toolkits: readonly string[],
): Map<string, Toolkit> {
	const unique = [...new Set(toolkits)];
	const results = useQueries({
		queries: unique.map((toolkit) => catalogToolkitQueryOptions(toolkit)),
	});
	const catalog = new Map<string, Toolkit>();
	unique.forEach((toolkit, index) => {
		const data = results[index]?.data;
		if (data) {
			catalog.set(toolkit, data);
		}
	});
	return catalog;
}
