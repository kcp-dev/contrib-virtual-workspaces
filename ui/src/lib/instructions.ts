// Producer-written instructions, made safe and made specific.
//
// A producer ships "how to use this" as text in a ConfigMap: paragraphs,
// headings, lists, code blocks, a few links. Two things happen to it here.
//
// It is made SPECIFIC: {{variables}} become the reader's own values — the
// server, their project, who they are — so a snippet can be copied and run
// rather than edited first. The set of variables is fixed and small, and
// an unknown one is left exactly as written, so a typo shows up on screen
// instead of vanishing.
//
// It is made SAFE: the text is parsed into a small block model and rendered
// as elements, never as HTML. A producer can write a code block or a link;
// a producer cannot write a <script>, an onclick, or a javascript: href.
// The format is a markdown subset for the same reason — what is supported
// is exactly what setup instructions need, and nothing that needs
// sanitising.

export type Span =
  | { kind: 'text'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'strong'; text: string }
  | { kind: 'link'; text: string; href: string }

export type Block =
  | { kind: 'heading'; level: 1 | 2 | 3; spans: Span[] }
  | { kind: 'paragraph'; spans: Span[] }
  | { kind: 'code'; lang: string; text: string }
  | { kind: 'list'; items: Span[][] }

export interface Section {
  key: string
  label: string
  text: string
}

/** Variables a producer may use. Documented in the marketplace README. */
export type Variables = Partial<
  Record<
    | 'server'
    | 'workspace'
    | 'cluster'
    | 'tenant'
    | 'project'
    | 'user'
    | 'export'
    | 'producerWorkspace'
    | 'binding'
    // Where the reader signs in, and as which OAuth client. For a producer
    // whose "how to use it" is pointing another program at the platform —
    // MCP — the client has to be told these, and the console already knows.
    | 'issuer'
    | 'clientId',
    string
  >
>

/** fill replaces {{name}} with its value; unknown names are left as-is. */
export function fill(text: string, vars: Variables): string {
  return text.replace(/\{\{\s*([a-zA-Z]+)\s*\}\}/g, (whole, name: string) => {
    const v = (vars as Record<string, string | undefined>)[name]
    return v === undefined ? whole : v
  })
}

/**
 * sections orders a ConfigMap's keys and derives labels: a leading `NN-`
 * orders and is dropped, a `.md` suffix is dropped, dashes become spaces
 * and words are capitalised. "10-claude-code.md" → "Claude Code".
 */
export function sections(data: Record<string, string> | undefined): Section[] {
  if (!data) return []
  return Object.keys(data)
    .sort()
    .map((key) => ({ key, label: labelOf(key), text: data[key] ?? '' }))
}

function labelOf(key: string): string {
  const bare = key.replace(/^\d+-/, '').replace(/\.(md|txt)$/i, '')
  return bare
    .split(/[-_]+/)
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

/** parse turns the markdown subset into blocks. Never throws. */
export function parse(text: string): Block[] {
  const blocks: Block[] = []
  const lines = text.replace(/\r\n/g, '\n').split('\n')
  let i = 0
  let para: string[] = []
  let list: string[] = []

  const flushPara = () => {
    if (para.length) {
      blocks.push({ kind: 'paragraph', spans: inline(para.join(' ')) })
      para = []
    }
  }
  const flushList = () => {
    if (list.length) {
      blocks.push({ kind: 'list', items: list.map(inline) })
      list = []
    }
  }

  while (i < lines.length) {
    const line = lines[i]

    // Fenced code: everything until the closing fence, verbatim.
    const fence = /^```\s*([a-zA-Z0-9_-]*)\s*$/.exec(line)
    if (fence) {
      flushPara()
      flushList()
      const lang = fence[1] ?? ''
      const body: string[] = []
      i++
      while (i < lines.length && !/^```\s*$/.test(lines[i])) {
        body.push(lines[i])
        i++
      }
      i++ // the closing fence, if any
      blocks.push({ kind: 'code', lang, text: body.join('\n') })
      continue
    }

    const heading = /^(#{1,3})\s+(.*)$/.exec(line)
    if (heading) {
      flushPara()
      flushList()
      blocks.push({ kind: 'heading', level: heading[1].length as 1 | 2 | 3, spans: inline(heading[2]) })
      i++
      continue
    }

    const item = /^\s*[-*]\s+(.*)$/.exec(line)
    if (item) {
      flushPara()
      list.push(item[1])
      i++
      // Continuation lines are indented; join them onto the item.
      while (i < lines.length && /^\s{2,}\S/.test(lines[i]) && !/^\s*[-*]\s+/.test(lines[i])) {
        list[list.length - 1] += ' ' + lines[i].trim()
        i++
      }
      continue
    }

    if (line.trim() === '') {
      flushPara()
      flushList()
      i++
      continue
    }

    flushList()
    para.push(line.trim())
    i++
  }
  flushPara()
  flushList()
  return blocks
}

/**
 * inline splits a line into spans: `code`, **strong**, and [text](href)
 * where href is http(s) only. Anything else is text, including a link
 * with a scheme that is not allowed — shown as written, not as a link.
 */
export function inline(text: string): Span[] {
  const spans: Span[] = []
  const re = /`([^`]+)`|\*\*([^*]+)\*\*|\[([^\]]+)\]\(([^)\s]+)\)/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) spans.push({ kind: 'text', text: text.slice(last, m.index) })
    if (m[1] !== undefined) {
      spans.push({ kind: 'code', text: m[1] })
    } else if (m[2] !== undefined) {
      spans.push({ kind: 'strong', text: m[2] })
    } else if (m[3] !== undefined && m[4] !== undefined) {
      if (/^https?:\/\//i.test(m[4])) spans.push({ kind: 'link', text: m[3], href: m[4] })
      else spans.push({ kind: 'text', text: m[0] })
    }
    last = m.index + m[0].length
  }
  if (last < text.length) spans.push({ kind: 'text', text: text.slice(last) })
  return spans
}

/** render is fill then parse: the whole pipeline for one section. */
export function render(text: string, vars: Variables): Block[] {
  return parse(fill(text, vars))
}
