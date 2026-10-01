// Producer-written text is rendered to people, so what this refuses to
// turn into markup matters as much as what it does.

import { describe, expect, it } from 'vitest'
import { fill, inline, parse, sections } from './instructions'

describe('fill', () => {
  it('replaces known variables and leaves unknown ones visible', () => {
    expect(fill('at {{server}}/x as {{user}}', { server: 'https://k', user: 'alice' })).toBe(
      'at https://k/x as alice',
    )
    // A typo should show on screen, not vanish.
    expect(fill('{{servr}}', { server: 'https://k' })).toBe('{{servr}}')
  })

  it('tolerates spaces inside the braces', () => {
    expect(fill('{{ project }}', { project: 'web' })).toBe('web')
  })
})

describe('sections', () => {
  it('orders by the numeric prefix and derives a label from the rest', () => {
    const s = sections({ '20-claude-desktop.md': 'b', '00-overview.md': 'a', '10-claude-code.md': 'c' })
    expect(s.map((x) => x.label)).toEqual(['Overview', 'Claude Code', 'Claude Desktop'])
  })

  it('treats no instructions as no sections', () => {
    expect(sections(undefined)).toEqual([])
  })
})

describe('parse', () => {
  it('keeps a fenced code block verbatim, with its language', () => {
    const [block] = parse('```sh\nclaude mcp add x\n  indented\n```')
    expect(block).toEqual({ kind: 'code', lang: 'sh', text: 'claude mcp add x\n  indented' })
  })

  it('joins paragraph lines and separates on blank lines', () => {
    const blocks = parse('one\ntwo\n\nthree')
    expect(blocks).toHaveLength(2)
    expect(blocks[0]).toMatchObject({ kind: 'paragraph', spans: [{ kind: 'text', text: 'one two' }] })
  })

  it('reads headings and lists', () => {
    const blocks = parse('## Title\n- a\n- b\n  continued')
    expect(blocks[0]).toMatchObject({ kind: 'heading', level: 2 })
    expect(blocks[1]).toMatchObject({ kind: 'list' })
    expect((blocks[1] as { items: unknown[] }).items).toHaveLength(2)
    expect(blocks[1]).toMatchObject({ items: [[{ text: 'a' }], [{ text: 'b continued' }]] })
  })

  it('does not throw on an unclosed fence', () => {
    expect(parse('```\nnever closed')).toEqual([{ kind: 'code', lang: '', text: 'never closed' }])
  })
})

describe('inline', () => {
  it('makes code, strong and http links into spans', () => {
    expect(inline('run `x` **now** at [docs](https://d)')).toEqual([
      { kind: 'text', text: 'run ' },
      { kind: 'code', text: 'x' },
      { kind: 'text', text: ' ' },
      { kind: 'strong', text: 'now' },
      { kind: 'text', text: ' at ' },
      { kind: 'link', text: 'docs', href: 'https://d' },
    ])
  })

  // The security property: a producer cannot make the console run or
  // navigate to anything but http(s).
  it('refuses links with any scheme but http(s), showing them as text', () => {
    // The invariant is "no link span, and nothing lost" — not where the
    // text happens to be split. A nested paren splits it; that is fine.
    for (const input of ['[x](javascript:alert(1))', '[x](data:text/html,hi)', '[x](vbscript:x)']) {
      const spans = inline(input)
      expect(spans.every((s) => s.kind === 'text')).toBe(true)
      expect(spans.map((s) => s.text).join('')).toBe(input)
    }
  })

  it('never produces markup from angle brackets', () => {
    // There is no span kind for raw HTML, so this can only be text.
    expect(inline('<script>alert(1)</script>')).toEqual([{ kind: 'text', text: '<script>alert(1)</script>' }])
  })
})
