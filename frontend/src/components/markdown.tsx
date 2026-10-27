import ReactMarkdown, { type Components } from "react-markdown";

import { cn } from "@/lib/utils";

// Links leave the app; raw HTML in the text is never rendered.
const components: Components = {
  a: ({ node: _, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer" />,
};

/** Message or summary text, rendered as Markdown without raw HTML. */
export function Markdown({ text, className }: { text: string; className?: string }) {
  return (
    <div className={cn("md", className)}>
      <ReactMarkdown components={components} skipHtml>
        {text}
      </ReactMarkdown>
    </div>
  );
}
