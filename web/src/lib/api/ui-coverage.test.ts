import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import ts from "typescript";
import { describe, expect, it } from "vitest";

interface ClientModule {
	dependencies: Map<string, Set<string>>;
	file: string;
	moduleSpecifier: string;
	transports: Set<string>;
}

const sourceRoot = path.resolve(process.cwd(), "src");
const libRoot = path.join(sourceRoot, "lib");

function typescriptFiles(directory: string): string[] {
	return readdirSync(directory).flatMap((name) => {
		const file = path.join(directory, name);
		if (statSync(file).isDirectory()) return typescriptFiles(file);
		return /\.(?:ts|tsx)$/.test(name) ? [file] : [];
	});
}

const allSourceFiles = typescriptFiles(sourceRoot);
const productionSourceFiles = allSourceFiles.filter(
	(file) => !/\.test\.(?:ts|tsx)$/.test(file),
);
const testSourceFiles = allSourceFiles.filter((file) =>
	/\.test\.(?:ts|tsx)$/.test(file),
);

function clientModules(): ClientModule[] {
	return readdirSync(libRoot)
		.filter((domain) => domain !== "api")
		.map((domain) => ({
			file: path.join(libRoot, domain, "client.ts"),
			moduleSpecifier: `@/lib/${domain}/client`,
		}))
		.filter(({ file }) => {
			try {
				return statSync(file).isFile();
			} catch {
				return false;
			}
		})
		.map(({ file, moduleSpecifier }) => analyzeClient(file, moduleSpecifier));
}

function analyzeClient(file: string, moduleSpecifier: string): ClientModule {
	const source = ts.createSourceFile(
		file,
		readFileSync(file, "utf8"),
		ts.ScriptTarget.Latest,
		true,
		file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
	);
	const declarations: { name: string; node: ts.Node }[] = [];
	for (const statement of source.statements) {
		const exported =
			ts.canHaveModifiers(statement) &&
			ts
				.getModifiers(statement)
				?.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword);
		if (!exported) continue;
		if (
			ts.isFunctionDeclaration(statement) &&
			statement.name &&
			statement.body
		) {
			declarations.push({ name: statement.name.text, node: statement.body });
		}
		if (ts.isVariableStatement(statement)) {
			for (const declaration of statement.declarationList.declarations) {
				if (ts.isIdentifier(declaration.name) && declaration.initializer) {
					declarations.push({
						name: declaration.name.text,
						node: declaration.initializer,
					});
				}
			}
		}
	}
	const exported = new Set(declarations.map((declaration) => declaration.name));
	const dependencies = new Map<string, Set<string>>();
	const transports = new Set<string>();

	for (const declaration of declarations) {
		const { name } = declaration;
		const used = new Set<string>();
		walk(declaration.node, (node) => {
			if (
				ts.isIdentifier(node) &&
				exported.has(node.text) &&
				node.text !== name
			) {
				used.add(node.text);
			}
			if (
				ts.isCallExpression(node) &&
				ts.isIdentifier(node.expression) &&
				["apiFetch", "apiRequest"].includes(node.expression.text)
			) {
				transports.add(name);
			}
		});
		dependencies.set(name, used);
	}

	return { dependencies, file, moduleSpecifier, transports };
}

function walk(node: ts.Node, visit: (candidate: ts.Node) => void) {
	visit(node);
	node.forEachChild((child) => walk(child, visit));
}

function importedRoots(module: ClientModule, files: string[]): Set<string> {
	const roots = new Set<string>();
	for (const file of files) {
		if (file === module.file) continue;
		const source = ts.createSourceFile(
			file,
			readFileSync(file, "utf8"),
			ts.ScriptTarget.Latest,
			true,
			file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
		);
		for (const statement of source.statements) {
			if (
				!ts.isImportDeclaration(statement) ||
				!ts.isStringLiteral(statement.moduleSpecifier) ||
				!statement.importClause?.namedBindings ||
				!ts.isNamedImports(statement.importClause.namedBindings)
			) {
				continue;
			}
			if (!importsClient(file, statement.moduleSpecifier.text, module)) {
				continue;
			}
			for (const element of statement.importClause.namedBindings.elements) {
				roots.add(element.propertyName?.text ?? element.name.text);
			}
		}
	}
	return roots;
}

function importsClient(
	from: string,
	specifier: string,
	module: ClientModule,
): boolean {
	if (specifier === module.moduleSpecifier) return true;
	if (!specifier.startsWith(".")) return false;
	const resolved = path.resolve(path.dirname(from), specifier);
	return [resolved, `${resolved}.ts`, path.join(resolved, "index.ts")].includes(
		module.file,
	);
}

function reachable(module: ClientModule, roots: Set<string>): Set<string> {
	const result = new Set<string>();
	const queue = [...roots];
	while (queue.length > 0) {
		const current = queue.pop();
		if (!current || result.has(current)) continue;
		result.add(current);
		for (const dependency of module.dependencies.get(current) ?? []) {
			queue.push(dependency);
		}
	}
	return result;
}

describe("frontend API reachability", () => {
	it("keeps every domain transport reachable from a non-test UI or service surface", () => {
		const uncovered = clientModules().flatMap((module) => {
			const roots = importedRoots(module, productionSourceFiles);
			const used = reachable(module, roots);
			const domain = path.basename(path.dirname(module.file));
			return [...module.transports]
				.filter((operation) => !used.has(operation))
				.map((operation) => `${domain}.${operation}`);
		});

		expect(
			uncovered,
			"Every API operation must remain reachable from a real frontend surface",
		).toEqual([]);
	});

	it("keeps every domain transport under a client contract test", () => {
		const uncovered = clientModules().flatMap((module) => {
			const roots = importedRoots(module, testSourceFiles);
			const used = reachable(module, roots);
			const domain = path.basename(path.dirname(module.file));
			return [...module.transports]
				.filter((operation) => !used.has(operation))
				.map((operation) => `${domain}.${operation}`);
		});

		expect(
			uncovered,
			"Every API operation must be exercised through a client contract test",
		).toEqual([]);
	});
});
