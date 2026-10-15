import { useId } from "react"
import type { ComponentProps, ReactNode } from "react"
import { Check } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"

/*
 * The settings kit: every Settings page is built from these (DESIGN.md §3,
 * "Settings as a full page"). A page is a heading and a subtitle, then groups;
 * a group is a quiet label over one card; a card is rows, each a title and
 * an explanation on the left and its control on the right. The controls are
 * the library's (switch, select, badge, button), dressed from theme tokens in
 * workspace-settings.css.
 */

/** A page's title (h2, under the "Settings · org" header) and its one line. */
export function SettingsHeading({
  id,
  title,
  subtitle,
}: {
  id?: string
  title: string
  subtitle?: ReactNode
}) {
  return (
    <div className="settings-heading">
      <h2 id={id}>{title}</h2>
      {subtitle && <p>{subtitle}</p>}
    </div>
  )
}

/** A labelled group of rows on one card. `bare` leaves the card out. */
export function SettingsGroup({
  label,
  children,
  bare = false,
  className,
}: {
  label: string
  children: ReactNode
  bare?: boolean
  className?: string
}) {
  const id = useId()
  return (
    <section className={cn("settings-group", className)} aria-labelledby={id}>
      <h3 id={id}>{label}</h3>
      {bare ? children : <SettingsCard>{children}</SettingsCard>}
    </section>
  )
}

export function SettingsCard({
  className,
  ...props
}: ComponentProps<typeof Card>) {
  return <Card className={cn("settings-card", className)} {...props} />
}

/**
 * One row: an optional mark, the title and what it means, and its controls.
 * `tone="danger"` marks a destructive row's title.
 */
export function SettingsRow({
  title,
  description,
  media,
  children,
  tone,
  className,
}: {
  title?: ReactNode
  description?: ReactNode
  media?: ReactNode
  children?: ReactNode
  tone?: "danger"
  className?: string
}) {
  return (
    <div className={cn("settings-row", className)} data-tone={tone}>
      {media && <span className="settings-row-media">{media}</span>}
      <div className="settings-row-text">
        {title && <strong>{title}</strong>}
        {description && <span>{description}</span>}
      </div>
      {children && <div className="settings-row-control">{children}</div>}
    </div>
  )
}

/** A row's own content in full width under its title (chips, a form). */
export function SettingsRowBody({
  className,
  ...props
}: ComponentProps<"div">) {
  return <div className={cn("settings-row-body", className)} {...props} />
}

/** The library's switch, named for the row it sits in. */
export function SettingsSwitch({
  label,
  checked,
  onCheckedChange,
  disabled,
}: {
  label: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
  disabled?: boolean
}) {
  return (
    <Switch
      className="settings-switch"
      aria-label={label}
      checked={checked}
      disabled={disabled}
      onCheckedChange={(next) => onCheckedChange(next)}
    />
  )
}

export type SettingsOption<T extends string> = { value: T; label: string }

/** The library's select, showing the chosen option's words. */
export function SettingsSelect<T extends string>({
  label,
  value,
  options,
  onValueChange,
  disabled,
  className,
}: {
  label: string
  value: T
  options: readonly SettingsOption<T>[]
  onValueChange: (value: T) => void
  disabled?: boolean
  className?: string
}) {
  return (
    <Select
      items={options}
      value={value}
      disabled={disabled}
      onValueChange={(next) => {
        if (next !== null) onValueChange(next)
      }}
    >
      <SelectTrigger
        className={cn("settings-select", className)}
        aria-label={label}
        size="sm"
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent className="settings-popup" alignItemWithTrigger={false}>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            {option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** A row of quiet chips (what is masked, which senders): a list to read. */
export function SettingsChips({
  label,
  items,
}: {
  label: string
  items: readonly string[]
}) {
  return (
    <ul className="settings-chips" aria-label={label}>
      {items.map((item) => (
        <li key={item}>
          <Badge variant="outline" className="settings-chip">
            {item}
          </Badge>
        </li>
      ))}
    </ul>
  )
}

/** "Connected" and other good states, beside a row's controls. */
export function SettingsStatus({ children }: { children: ReactNode }) {
  return (
    <span className="settings-status">
      <Check size={14} strokeWidth={2} aria-hidden="true" />
      {children}
    </span>
  )
}

/** A value shown as it is: an address, a domain, a region. */
export function SettingsValue({
  children,
  mono = false,
}: {
  children: ReactNode
  mono?: boolean
}) {
  return (
    <span className="settings-value" data-mono={mono ? "true" : undefined}>
      {children}
    </span>
  )
}

/** A row's button: quiet by default, `danger` for a destructive action. */
export function SettingsButton({
  tone,
  className,
  ...props
}: Omit<ComponentProps<typeof Button>, "variant" | "size"> & {
  tone?: "primary" | "danger"
}) {
  return (
    <Button
      type="button"
      variant={
        tone === "danger"
          ? "destructive"
          : tone === "primary"
            ? "default"
            : "outline"
      }
      size="sm"
      className={cn("settings-button", className)}
      data-tone={tone}
      {...props}
    />
  )
}

/** One honest line where a service or a route is not there yet. */
export function SettingsNote({ children }: { children: ReactNode }) {
  return <p className="settings-note">{children}</p>
}

/** A result or a failure, said where it happened. */
export function SettingsMessage({
  tone,
  children,
}: {
  tone: "error" | "notice"
  children: ReactNode
}) {
  return (
    <p
      className="settings-message"
      data-tone={tone}
      role={tone === "error" ? "alert" : "status"}
    >
      {children}
    </p>
  )
}
