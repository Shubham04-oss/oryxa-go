interface GoogleButtonProps {
	returnTo: string;
}

export function GoogleButton({ returnTo }: GoogleButtonProps) {
	function continueWithGoogle() {
		const login = new URL(
			"/v1/auth/login",
			import.meta.env.VITE_AUTH_API_URL ?? window.location.origin,
		);
		login.searchParams.set(
			"return_to",
			new URL(returnTo, window.location.origin).toString(),
		);
		window.location.assign(login);
	}

	return (
		<Button
			className="auth-button auth-button-secondary"
			size="lg"
			type="button"
			variant="outline"
			onClick={continueWithGoogle}
		>
			Continue with Google
		</Button>
	);
}

import { Button } from "@/components/ui/button";
