/** Convert rich HTML descriptions to readable text for compact product summaries. */
export function htmlToPlainText(value: unknown): string {
    if (value == null) return ''

    const html = String(value).trim()
    if (!html) return ''

    if (typeof DOMParser !== 'undefined') {
        const parsed = new DOMParser().parseFromString(html, 'text/html')
        parsed.body.querySelectorAll('script, style, template, noscript').forEach((node) => node.remove())

        let text = ''
        const visit = (node: Node) => {
            if (node.nodeType === Node.TEXT_NODE) {
                text += node.textContent || ''
                return
            }

            if (node.nodeType !== Node.ELEMENT_NODE) return

            const element = node as HTMLElement
            const tag = element.tagName.toLowerCase()
            if (tag === 'br') {
                text += '\n'
                return
            }
            if (tag === 'li') text += '\n• '
            for (const child of Array.from(element.childNodes)) visit(child)
            if (['address', 'article', 'blockquote', 'div', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'li', 'ol', 'p', 'section', 'ul'].includes(tag)) {
                text += '\n'
            }
        }

        visit(parsed.body)
        return text.replace(/[ \t]*\n[ \t]*/g, '\n').replace(/\n{2,}/g, '\n').trim()
    }

    // Keep descriptions readable in non-browser contexts where DOMParser is unavailable.
    return html
        .replace(/<(script|style|template|noscript)\b[^>]*>[\s\S]*?<\/\1\s*>/gi, '')
        .replace(/<br\s*\/?\s*>/gi, '\n')
        .replace(/<li\b[^>]*>/gi, '\n• ')
        .replace(/<\/(?:address|article|blockquote|div|h[1-6]|li|ol|p|section|ul)\s*>/gi, '\n')
        .replace(/<[^>]*>/g, '')
        .replace(/&nbsp;|&#160;/gi, ' ')
        .replace(/&amp;/gi, '&')
        .replace(/&lt;/gi, '<')
        .replace(/&gt;/gi, '>')
        .replace(/&quot;/gi, '"')
        .replace(/&#39;|&apos;/gi, "'")
        .replace(/[ \t]*\n[ \t]*/g, '\n')
        .replace(/\n{2,}/g, '\n')
        .trim()
}
