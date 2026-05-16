package mcpx

import "strings"

// NameSeparator separates the upstream name from the upstream-local
// identifier in the proxy's exported tool/prompt names.
const NameSeparator = "__"

// PrefixName joins upstream and name with NameSeparator.
func PrefixName(upstream, name string) string {
	return upstream + NameSeparator + name
}

// SplitName recovers (upstream, name) from a prefixed identifier.
func SplitName(prefixed string) (upstream, name string, ok bool) {
	upstream, name, ok = strings.Cut(prefixed, NameSeparator)
	if !ok || upstream == "" {
		return "", "", false
	}
	return upstream, name, true
}
