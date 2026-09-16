import { Eye, EyeOff } from "lucide-react";
import { type ComponentProps, type Ref, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

interface FormFieldProps
	extends Omit<ComponentProps<typeof Input>, "id" | "ref"> {
	autoComplete: string;
	error?: string;
	id: string;
	inputRef?: Ref<HTMLInputElement>;
	label: string;
}

export function FormField({
	autoComplete,
	error,
	id,
	inputRef,
	label,
	name,
	placeholder,
	type = "text",
	...inputProps
}: FormFieldProps) {
	const errorId = `${id}-error`;
	const isPassword = type === "password";
	const [isPasswordVisible, setIsPasswordVisible] = useState(false);
	const inputType = isPassword && isPasswordVisible ? "text" : type;

	return (
		<div className="field">
			<Label className="field-label" htmlFor={id}>
				{label}
			</Label>
			<div className="field-control">
				<Input
					className={`field-input${isPassword ? " field-input-password" : ""}`}
					id={id}
					ref={inputRef}
					name={name}
					type={inputType}
					autoComplete={autoComplete}
					placeholder={placeholder}
					aria-describedby={error ? errorId : undefined}
					aria-invalid={Boolean(error)}
					{...inputProps}
				/>
				{isPassword ? (
					<Button
						className="password-visibility"
						size="icon-sm"
						type="button"
						variant="ghost"
						onClick={() => setIsPasswordVisible((visible) => !visible)}
						aria-label={isPasswordVisible ? "Hide password" : "Show password"}
						aria-pressed={isPasswordVisible}
					>
						{isPasswordVisible ? <EyeOff /> : <Eye />}
					</Button>
				) : null}
			</div>
			{error ? (
				<p className="field-error" id={errorId}>
					{error}
				</p>
			) : null}
		</div>
	);
}
