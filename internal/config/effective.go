package config

import (
	"fmt"
	"sort"
	"strings"
)

// EffectiveParam is one resolved parameter of an EffectiveDetector.
type EffectiveParam struct {
	Key   string
	Value any
	// Source is "config" when the value comes from the Config, "constructor"
	// when the detector will fall back to its compiled default.
	Source string
	Type   ParamType
	Unit   string
}

// EffectiveDetector is the merged, fully resolved configuration of one
// detector: what will actually run when the binary builds it.
type EffectiveDetector struct {
	ID          string
	Name        string
	Category    string
	Configured  bool // false when the Config has no entry (defaults apply)
	Enabled     bool
	Mode        string
	Shadow      bool // Mode == "shadow" or listed in shadow.shadow_detectors
	Weight      float64
	AutoEnforce bool
	Params      []EffectiveParam
	// Unknown lists params keys that no spec accepts (they are errors for
	// Validate; listed here so a hand-built Config still prints completely).
	Unknown []string
}

// EffectiveTable returns the merged per-detector table for every detector the
// config layer knows about (plus any extra IDs present in cfg.Detectors),
// sorted by ID. Params are resolved against the constructor fallbacks in
// params.go, so a key missing from the config is shown with the value the
// detector will really use. Binaries print this at startup so moderators can
// verify their edits took effect.
func (c *Config) EffectiveTable() []EffectiveDetector {
	ids := KnownDetectorIDs()
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for id := range c.Detectors {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	shadowList := make(map[string]bool, len(c.Shadow.ShadowDetectors))
	for _, id := range c.Shadow.ShadowDetectors {
		shadowList[id] = true
	}

	out := make([]EffectiveDetector, 0, len(ids))
	for _, id := range ids {
		dc, configured := c.Detectors[id]
		if !configured {
			dc = c.GetDetectorConfig(id)
		}
		spec, known := detectorSpecs[id]
		row := EffectiveDetector{
			ID:          id,
			Name:        spec.Name,
			Category:    spec.Category,
			Configured:  configured,
			Enabled:     dc.Enabled,
			Mode:        dc.Mode,
			Shadow:      dc.Mode == "shadow" || shadowList[id],
			Weight:      dc.EnforcementWeight,
			AutoEnforce: dc.AutoEnforce,
		}
		if !known {
			row.Name = "(unknown detector)"
			for k := range dc.Params {
				row.Unknown = append(row.Unknown, k)
			}
			sort.Strings(row.Unknown)
			out = append(out, row)
			continue
		}
		consumed := make(map[string]bool)
		for _, ps := range spec.Params {
			ep := EffectiveParam{Key: ps.Key, Type: ps.Type, Unit: ps.Unit, Value: ps.Default, Source: "constructor"}
			if v, ok := dc.Params[ps.Key]; ok {
				consumed[ps.Key] = true
				if norm, err := normalizeParamValue(ps, v); err == nil {
					ep.Value = norm
				} else {
					ep.Value = v
				}
				ep.Source = "config"
			} else {
				for _, alias := range ps.Aliases {
					if v, ok := dc.Params[alias]; ok {
						consumed[alias] = true
						if norm, err := normalizeParamValue(ps, v); err == nil {
							ep.Value = norm
						} else {
							ep.Value = v
						}
						ep.Source = "config (alias " + alias + ")"
						break
					}
				}
			}
			row.Params = append(row.Params, ep)
		}
		for k := range dc.Params {
			if consumed[k] {
				continue
			}
			if _, removed := spec.Removed[k]; removed {
				continue // dropped with a warning at load/validate time
			}
			if _, reserved := reservedParamKeys[k]; reserved {
				continue
			}
			row.Unknown = append(row.Unknown, k)
		}
		sort.Strings(row.Unknown)
		out = append(out, row)
	}
	return out
}

// FormatEffectiveTable renders the table as fixed-width text, one line per
// detector followed by an indented line of resolved params. Params that fall
// back to the constructor default are marked with an asterisk.
func FormatEffectiveTable(rows []EffectiveDetector) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-10s %-8s %-8s %-7s %-5s %s\n", "DETECTOR", "ENABLED", "MODE", "WEIGHT", "AUTO", "NAME")
	for _, r := range rows {
		enabled := "off"
		if r.Enabled {
			enabled = "on"
		}
		mode := r.Mode
		if mode == "" {
			mode = "(none)"
		}
		if r.Shadow && r.Mode != "shadow" {
			mode += "+shadow"
		}
		auto := "no"
		if r.AutoEnforce {
			auto = "yes"
		}
		fmt.Fprintf(&b, "%-10s %-8s %-8s %-7.2f %-5s %s\n", r.ID, enabled, mode, r.Weight, auto, r.Name)
		if len(r.Params) > 0 {
			parts := make([]string, 0, len(r.Params))
			for _, p := range r.Params {
				mark := ""
				if p.Source != "config" {
					mark = "*"
				}
				parts = append(parts, fmt.Sprintf("%s=%v%s", p.Key, formatParamValue(p.Value), mark))
			}
			fmt.Fprintf(&b, "           %s\n", strings.Join(parts, " "))
		}
		if len(r.Unknown) > 0 {
			fmt.Fprintf(&b, "           UNKNOWN KEYS: %s\n", strings.Join(r.Unknown, " "))
		}
	}
	b.WriteString("(* = constructor fallback; key absent from config)\n")
	return b.String()
}

func formatParamValue(v any) string {
	switch val := v.(type) {
	case []string:
		if len(val) == 0 {
			return "[]"
		}
		return "[" + strings.Join(val, ",") + "]"
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", val), "0"), ".")
	default:
		return fmt.Sprintf("%v", v)
	}
}
