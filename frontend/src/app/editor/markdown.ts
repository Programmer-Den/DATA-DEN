import { Marked } from 'marked';

const marked = new Marked({ gfm: true, breaks: true, async: false });

/**
 * Markdown as HTML, for [innerHTML]: Angular's sanitizer then removes
 * scripts, event handlers and javascript: links that the text might hold.
 */
export function markdownHtml(text: string): string {
  return marked.parse(text) as string;
}

/** True for a file shown and edited as Markdown. */
export function isMarkdown(name: string): boolean {
  return name.toLowerCase().endsWith('.md');
}
