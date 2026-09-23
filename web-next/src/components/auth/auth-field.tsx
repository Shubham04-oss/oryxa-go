import { useState } from "react"
import { Eye, EyeOff } from "lucide-react"

type AuthFieldProps = {
  autoComplete: string
  error?: string
  label: string
  name: string
  placeholder?: string
  type?: "email" | "password" | "text"
}

export function AuthField({
  autoComplete,
  error,
  label,
  name,
  placeholder,
  type = "text",
}: AuthFieldProps) {
  const [revealed, setRevealed] = useState(false)
  const isPassword = type === "password"
  return (
    <div className="auth-field">
      <label htmlFor={`auth-${name}`}>{label}</label>
      <div className="auth-input-wrap">
        <input
          id={`auth-${name}`}
          name={name}
          type={isPassword && revealed ? "text" : type}
          autoComplete={autoComplete}
          placeholder={placeholder}
          aria-invalid={Boolean(error)}
          aria-describedby={error ? `auth-${name}-error` : undefined}
        />
        {isPassword && (
          <button
            type="button"
            className="auth-reveal"
            aria-label={revealed ? "Hide password" : "Show password"}
            onClick={() => setRevealed((current) => !current)}
          >
            {revealed ? <EyeOff size={17} /> : <Eye size={17} />}
          </button>
        )}
      </div>
      {error && <small id={`auth-${name}-error`}>{error}</small>}
    </div>
  )
}
