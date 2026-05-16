package mcpx

import "strings"

// PrefixURI rewrites <scheme>:<rest> as <upstream>+<scheme>:<rest>.
// Operates on the scheme prefix only so it works on RFC 6570 URI
// templates (e.g. "repo://{owner}/...") that net/url rejects.
// Returns "" if uri is missing a syntactically valid scheme.
func PrefixURI(upstream, uri string) string {
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok || !validScheme(scheme) {
		return ""
	}
	return upstream + "+" + scheme + ":" + rest
}

// SplitURI is the inverse of PrefixURI.
func SplitURI(uri string) (upstream, original string, ok bool) {
	scheme, rest, ok := strings.Cut(uri, ":")
	if !ok {
		return "", "", false
	}
	upstream, originalScheme, ok := strings.Cut(scheme, "+")
	if !ok || upstream == "" || !validScheme(originalScheme) {
		return "", "", false
	}
	return upstream, originalScheme + ":" + rest, true
}

// validScheme reports whether s is a syntactically valid URI scheme per
// RFC 3986: ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ).
func validScheme(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.'):
		default:
			return false
		}
	}
	return true
}
