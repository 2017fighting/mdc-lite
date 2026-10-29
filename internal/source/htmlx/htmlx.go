// Package htmlx is the small HTML tree vocabulary the HTML sources share.
//
// Every source here reads a site's page with one parser and the same three
// questions: "where is the element with this class", "what is its attribute
// value", and "what does its text say". Those are not a scraping framework and
// not a CSS selector engine — they are four functions, kept in one place so
// javbus and fc2 do not grow two slightly different ideas of what the text of a
// subtree is.
package htmlx

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// Parse reads a document. It is the only place the package couples to x/net/html's
// constructor, so a source that has already got bytes never touches the library
// directly.
func Parse(body []byte) (*html.Node, error) {
	return html.Parse(bytes.NewReader(body))
}

// Attr returns an element's attribute value, or "" when it has none.
func Attr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// HasClass reports whether an element carries a class. The value is split the way
// HTML splits it, so `items_article_info` does not match `xitems_article_info`.
func HasClass(n *html.Node, class string) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	for _, c := range strings.Fields(Attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// Class is the predicate form of HasClass, for Find and FindAll.
func Class(class string) func(*html.Node) bool {
	return func(n *html.Node) bool { return HasClass(n, class) }
}

// Tag is the predicate form of "an element with this tag name".
func Tag(name string) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == name }
}

// Find returns the first matching node in document order, or nil.
func Find(n *html.Node, pred func(*html.Node) bool) *html.Node {
	if pred(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := Find(c, pred); found != nil {
			return found
		}
	}
	return nil
}

// FindAll returns every matching node in document order.
func FindAll(n *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if pred(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// Text is the concatenated text of a subtree with whitespace collapsed: a page's
// indentation is not part of any field's value.
func Text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// TextAfter returns the text that follows a descendant, which is how an info
// line's value is read out of `p > span.header + value`.
func TextAfter(node, after *html.Node) string {
	var b strings.Builder
	seen := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == after {
			seen = true
			return
		}
		if n.Type == html.TextNode && seen {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	return b.String()
}

// LinkText returns the text of each link in a subtree, in order, skipping empty
// ones. Two links that differ only in whitespace are still two links: deduping
// belongs to the engine, not to a parser.
func LinkText(n *html.Node) []string {
	var out []string
	for _, a := range FindAll(n, Tag("a")) {
		if s := strings.TrimSpace(Text(a)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Remove deletes every matching descendant from the tree. It is how a page whose
// title carries an invisible anti-scraping span is read as the title it prints,
// without teaching Text to ignore things.
func Remove(n *html.Node, pred func(*html.Node) bool) {
	for _, victim := range FindAll(n, pred) {
		if victim.Parent != nil {
			victim.Parent.RemoveChild(victim)
		}
	}
}
