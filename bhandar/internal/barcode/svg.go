package barcode

import (
	"fmt"
	"strings"
)

// Rendering defaults, used by DefaultOptions and for zero Options fields.
const (
	defaultModule = 2
	defaultHeight = 60
	defaultMargin = 10
)

// Options controls how SVG lays out a barcode.
//
// The zero value Options{} selects every default, including the printed
// label. Because a Go bool cannot default to true, any other Options value
// is taken literally for Text: start from DefaultOptions() and change the
// fields you need to keep the label while overriding sizes. Module, Height
// and Margin fall back to their defaults when zero or negative.
type Options struct {
	Module int  // width of one module in px (default 2)
	Height int  // bar height in px (default 60)
	Text   bool // print the human-readable code under the bars (default true)
	Margin int  // quiet zone in modules (default 10)
}

// DefaultOptions returns the documented defaults: 2 px modules, 60 px bars,
// a 10-module quiet zone on each side and the label printed.
func DefaultOptions() Options {
	return Options{Module: defaultModule, Height: defaultHeight, Text: true, Margin: defaultMargin}
}

// withDefaults resolves the zero value and zero fields as documented on
// Options.
func (o Options) withDefaults() Options {
	if o == (Options{}) {
		return DefaultOptions()
	}
	if o.Module <= 0 {
		o.Module = defaultModule
	}
	if o.Height <= 0 {
		o.Height = defaultHeight
	}
	if o.Margin <= 0 {
		o.Margin = defaultMargin
	}
	return o
}

// SVG renders modules (true = bar) as a standalone SVG document: black bars
// on a white background, a quiet zone of o.Margin modules on each side and,
// when o.Text is set and label is non-empty, the label centred under the
// bars. The image is (len(modules)+2*o.Margin)*o.Module px wide and
// o.Height px tall plus the label band. Adjacent bar modules are merged into
// one <rect>, so the output holds exactly one <rect> per run of bars; the
// background is a <path>. The label is XML-escaped, so the result is always
// well-formed XML.
func SVG(modules []bool, label string, o Options) string {
	o = o.withDefaults()

	width := (len(modules) + 2*o.Margin) * o.Module
	height := o.Height

	// Label metrics scale with the module width so the text stays legible
	// on large prints; pad leaves room above the glyphs and for descenders.
	fontSize := 5 * o.Module
	if fontSize < 8 {
		fontSize = 8
	}
	pad := fontSize / 2
	showLabel := o.Text && label != ""
	if showLabel {
		height += fontSize + pad
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, width, height, width, height)
	fmt.Fprintf(&b, `<path d="M0 0h%dv%dH0z" fill="#fff"/>`, width, height)
	b.WriteString(`<g fill="#000">`)
	for i := 0; i < len(modules); {
		if !modules[i] {
			i++
			continue
		}
		j := i
		for j < len(modules) && modules[j] {
			j++
		}
		fmt.Fprintf(&b, `<rect x="%d" y="0" width="%d" height="%d"/>`, (o.Margin+i)*o.Module, (j-i)*o.Module, o.Height)
		i = j
	}
	b.WriteString(`</g>`)
	if showLabel {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="monospace" font-size="%d" text-anchor="middle" fill="#000">%s</text>`,
			width/2, o.Height+pad/2+fontSize, fontSize, escapeXML(label))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// Render draws code as an SVG, choosing the symbology by content: a valid
// EAN-13 is drawn with EAN13Modules, anything else with Code128B. The code
// itself is the label. It does not normalise code; callers that accept
// user input should apply NormalizeCode first. Errors come from Code128B
// (empty, too long or non-printable input).
func Render(code string, o Options) (string, error) {
	var (
		modules []bool
		err     error
	)
	if ValidEAN13(code) {
		modules, err = EAN13Modules(code)
	} else {
		modules, err = Code128B(code)
	}
	if err != nil {
		return "", err
	}
	return SVG(modules, code, o), nil
}

// escapeXML escapes the five XML special characters and drops control
// characters that are not allowed in XML 1.0 text.
func escapeXML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&apos;")
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			// not representable in XML 1.0; drop it
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
