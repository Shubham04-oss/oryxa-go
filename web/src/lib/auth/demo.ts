/** Local demo pools have no Google identity provider. */
export const passwordOnlyDemo =
	import.meta.env.VITE_AUTH_PASSWORD_ONLY === "true";
