// A file's kind as a small coloured tile with its extension (founder's
// redesign, 14 Oct 2026): the same badge wherever a file is listed or open.
export type FileKind =
  "pdf" | "sheet" | "slides" | "image" | "video" | "audio" | "code" | "doc"

const kinds: Record<string, FileKind> = {
  pdf: "pdf",
  xlsx: "sheet",
  xlsm: "sheet",
  xls: "sheet",
  csv: "sheet",
  pptx: "slides",
  ppsx: "slides",
  key: "slides",
  png: "image",
  jpg: "image",
  jpeg: "image",
  gif: "image",
  webp: "image",
  avif: "image",
  svg: "image",
  mp4: "video",
  mov: "video",
  webm: "video",
  mp3: "audio",
  m4a: "audio",
  wav: "audio",
  js: "code",
  jsx: "code",
  ts: "code",
  tsx: "code",
  py: "code",
  go: "code",
  rs: "code",
  json: "code",
  yaml: "code",
  yml: "code",
  sql: "code",
  sh: "code",
  html: "code",
  css: "code",
}

function extensionOf(name: string): string {
  const dot = name.lastIndexOf(".")
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : ""
}

export function fileKind(name: string): FileKind {
  return kinds[extensionOf(name)] ?? "doc"
}

/** The badge's text: the extension, at most four letters, or DOC. */
export function fileKindLabel(name: string): string {
  const extension = extensionOf(name)
  return extension ? extension.slice(0, 4).toUpperCase() : "DOC"
}

export function FileKindBadge({
  name,
  size = "default",
}: {
  name: string
  size?: "default" | "small"
}) {
  return (
    <span
      className="workspace-file-kind"
      data-kind={fileKind(name)}
      data-size={size}
      aria-hidden="true"
    >
      {fileKindLabel(name)}
    </span>
  )
}
