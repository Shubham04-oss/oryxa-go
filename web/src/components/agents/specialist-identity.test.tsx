import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { SpecialistAvatar } from "./specialist-identity";

describe("specialist identity", () => {
	it("keeps an avatar stable for the same specialist ID", () => {
		const before = renderToStaticMarkup(
			<SpecialistAvatar name="Revenue Analyst" seed="specialist-42" />,
		);
		const renamed = renderToStaticMarkup(
			<SpecialistAvatar name="Growth Analyst" seed="specialist-42" />,
		);
		const other = renderToStaticMarkup(
			<SpecialistAvatar name="Research Analyst" seed="specialist-99" />,
		);
		const generatedFace = (markup: string) =>
			markup.slice(markup.indexOf("<svg"));

		expect(generatedFace(before)).toContain("<svg");
		expect(generatedFace(before)).toBe(generatedFace(renamed));
		expect(generatedFace(before)).not.toBe(generatedFace(other));
	});
});
