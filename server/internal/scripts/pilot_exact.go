//go:build pilot_e2e

package scripts

// RenderExactID is a pilot-only deterministic renderer. Unlike RenderID it
// does not select variants or run colloquialization.
func (b *Bank) RenderExactID(id, category string, vars map[string]any) (string, bool) {
	if b == nil || id == "" || category == "" {
		return "", false
	}
	for _, tpl := range b.cats[category] {
		if tpl.ID != id || tpl.Category != category {
			continue
		}
		text := replaceVars(tpl.Template, vars)
		if !safeText(text) || placeholderRe.MatchString(text) {
			return "", false
		}
		return text, true
	}
	return "", false
}
