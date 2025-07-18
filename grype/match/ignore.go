package match

import (
	"regexp"

	"github.com/bmatcuk/doublestar/v2"

	"github.com/anchore/grype/grype/vulnerability"
	"github.com/anchore/grype/internal/log"
)

// IgnoreFilter implementations are used to filter matches, returning all applicable IgnoreRule(s) that applied,
// these could include an IgnoreRule with only a Reason value filled in for synthetically generated rules
type IgnoreFilter interface {
	IgnoreMatch(match Match) []IgnoreRule
}

// An IgnoredMatch is a vulnerability Match that has been ignored because one or more IgnoreRules applied to the match.
type IgnoredMatch struct {
	Match

	// AppliedIgnoreRules are the rules that were applied to the match that caused Grype to ignore it.
	AppliedIgnoreRules []IgnoreRule
}

// An IgnoreRule specifies criteria for a vulnerability match to meet in order
// to be ignored. Not all criteria (fields) need to be specified, but all
// specified criteria must be met by the vulnerability match in order for the
// rule to apply.
type IgnoreRule struct {
	Vulnerability    string            `yaml:"vulnerability" json:"vulnerability" mapstructure:"vulnerability"`
	IncludeAliases   bool              `yaml:"include-aliases" json:"include-aliases" mapstructure:"include-aliases"`
	Reason           string            `yaml:"reason" json:"reason" mapstructure:"reason"`
	Namespace        string            `yaml:"namespace" json:"namespace" mapstructure:"namespace"`
	FixState         string            `yaml:"fix-state" json:"fix-state" mapstructure:"fix-state"`
	Package          IgnoreRulePackage `yaml:"package" json:"package" mapstructure:"package"`
	VexStatus        string            `yaml:"vex-status" json:"vex-status" mapstructure:"vex-status"`
	VexJustification string            `yaml:"vex-justification" json:"vex-justification" mapstructure:"vex-justification"`
	MatchType        Type              `yaml:"match-type" json:"match-type" mapstructure:"match-type"`
}

// IgnoreRulePackage describes the Package-specific fields that comprise the IgnoreRule.
type IgnoreRulePackage struct {
	Name         string `yaml:"name" json:"name" mapstructure:"name"`
	Version      string `yaml:"version" json:"version" mapstructure:"version"`
	Language     string `yaml:"language" json:"language" mapstructure:"language"`
	Type         string `yaml:"type" json:"type" mapstructure:"type"`
	Location     string `yaml:"location" json:"location" mapstructure:"location"`
	UpstreamName string `yaml:"upstream-name" json:"upstream-name" mapstructure:"upstream-name"`
}

// ApplyIgnoreRules iterates through the provided matches and, for each match,
// determines if the match should be ignored, by evaluating if any of the
// provided IgnoreRules apply to the match. If any rules apply to the match, all
// applicable rules are attached to the Match to form an IgnoredMatch.
// ApplyIgnoreRules returns two collections: the matches that are not being
// ignored, and the matches that are being ignored.
func ApplyIgnoreRules(matches Matches, rules []IgnoreRule) (Matches, []IgnoredMatch) {
	matched, ignored := ApplyIgnoreFilters(matches.Sorted(), rules...)
	return NewMatches(matched...), ignored
}

// ApplyIgnoreFilters applies all the IgnoreFilter(s) to the provided set of matches,
// splitting the results into a set of matched matches and ignored matches
func ApplyIgnoreFilters[T IgnoreFilter](matches []Match, filters ...T) ([]Match, []IgnoredMatch) {
	var out []Match
	var ignoredMatches []IgnoredMatch

	for _, match := range matches {
		var applicableRules []IgnoreRule

		for _, filter := range filters {
			applicableRules = append(applicableRules, filter.IgnoreMatch(match)...)
		}

		if len(applicableRules) > 0 {
			ignoredMatches = append(ignoredMatches, IgnoredMatch{
				Match:              match,
				AppliedIgnoreRules: applicableRules,
			})

			continue
		}

		out = append(out, match)
	}

	return out, ignoredMatches
}

func (r IgnoreRule) IgnoreMatch(match Match) []IgnoreRule {
	// VEX rules are handled by the vex processor
	if r.VexStatus != "" {
		return nil
	}

	ignoreConditions := getIgnoreConditionsForRule(r)
	if len(ignoreConditions) == 0 {
		// this rule specifies no criteria, so it doesn't apply to the Match
		return nil
	}

	for _, condition := range ignoreConditions {
		if !condition(match) {
			// as soon as one rule criterion doesn't apply, we know this rule doesn't apply to the Match
			return nil
		}
	}

	// all criteria specified in the rule apply to this Match
	return []IgnoreRule{r}
}

// HasConditions returns true if the ignore rule has conditions
// that can cause a match to be ignored
func (r IgnoreRule) HasConditions() bool {
	return len(getIgnoreConditionsForRule(r)) == 0
}

// ignoreFilters implements match.IgnoreFilter on a slice of objects that implement the same interface
type ignoreFilters[T IgnoreFilter] []T

func (r ignoreFilters[T]) IgnoreMatch(match Match) []IgnoreRule {
	for _, rule := range r {
		ignores := rule.IgnoreMatch(match)
		if len(ignores) > 0 {
			return ignores
		}
	}
	return nil
}

var _ IgnoreFilter = (*ignoreFilters[IgnoreRule])(nil)

// An ignoreCondition is a function that returns a boolean indicating whether
// the given Match should be ignored.
type ignoreCondition func(match Match) bool

func getIgnoreConditionsForRule(rule IgnoreRule) []ignoreCondition {
	var ignoreConditions []ignoreCondition

	if v := rule.Vulnerability; v != "" {
		ignoreConditions = append(ignoreConditions, ifVulnerabilityApplies(v, rule.IncludeAliases))
	}

	if ns := rule.Namespace; ns != "" {
		ignoreConditions = append(ignoreConditions, ifNamespaceApplies(ns))
	}

	if n := rule.Package.Name; n != "" {
		ignoreConditions = append(ignoreConditions, ifPackageNameApplies(n))
	}

	if v := rule.Package.Version; v != "" {
		ignoreConditions = append(ignoreConditions, ifPackageVersionApplies(v))
	}

	if l := rule.Package.Language; l != "" {
		ignoreConditions = append(ignoreConditions, ifPackageLanguageApplies(l))
	}

	if t := rule.Package.Type; t != "" {
		ignoreConditions = append(ignoreConditions, ifPackageTypeApplies(t))
	}

	if l := rule.Package.Location; l != "" {
		ignoreConditions = append(ignoreConditions, ifPackageLocationApplies(l))
	}

	if fs := rule.FixState; fs != "" {
		ignoreConditions = append(ignoreConditions, ifFixStateApplies(fs))
	}

	if upstreamName := rule.Package.UpstreamName; upstreamName != "" {
		ignoreConditions = append(ignoreConditions, ifUpstreamPackageNameApplies(upstreamName))
	}

	if matchType := rule.MatchType; matchType != "" {
		ignoreConditions = append(ignoreConditions, ifMatchTypeApplies(matchType))
	}
	return ignoreConditions
}

func ifFixStateApplies(fs string) ignoreCondition {
	return func(match Match) bool {
		if fs == string(vulnerability.FixStateUnknown) &&
			match.Vulnerability.Fix.State == "" { // no fix state specified is effectively "unknown"
			return true
		}
		return fs == string(match.Vulnerability.Fix.State)
	}
}

func ifVulnerabilityApplies(vulnerability string, includeAliases bool) ignoreCondition {
	return func(match Match) bool {
		if vulnerability == match.Vulnerability.ID {
			return true
		}
		if includeAliases {
			for _, related := range match.Vulnerability.RelatedVulnerabilities {
				if vulnerability == related.ID {
					return true
				}
			}
		}
		return false
	}
}

func ifNamespaceApplies(namespace string) ignoreCondition {
	return func(match Match) bool {
		return namespace == match.Vulnerability.Namespace
	}
}

func packageNameRegex(packageName string) (*regexp.Regexp, error) {
	pattern := packageName
	if packageName[0] != '$' || packageName[len(packageName)-1] != '^' {
		pattern = "^" + packageName + "$"
	}
	return regexp.Compile(pattern)
}

func ifPackageNameApplies(name string) ignoreCondition {
	// with enough ignore rules, we could end up needlessly creating a lot of regexes, which is not ideal.
	// instead lets detect if the input string is a regex or not, and if it is, then compile it...
	// otherwise, we can just do a simple string comparison
	if isLikelyARegex(name) {
		pattern, err := packageNameRegex(name)
		if err != nil || pattern == nil {
			return func(Match) bool { return false }
		}

		return func(match Match) bool {
			return pattern.MatchString(match.Package.Name)
		}
	}
	return func(match Match) bool {
		return name == match.Package.Name
	}
}

func ifPackageVersionApplies(version string) ignoreCondition {
	return func(match Match) bool {
		return version == match.Package.Version
	}
}

func ifPackageLanguageApplies(language string) ignoreCondition {
	return func(match Match) bool {
		return language == string(match.Package.Language)
	}
}

func ifPackageTypeApplies(t string) ignoreCondition {
	return func(match Match) bool {
		return t == string(match.Package.Type)
	}
}

func ifPackageLocationApplies(location string) ignoreCondition {
	return func(match Match) bool {
		return ruleLocationAppliesToMatch(location, match)
	}
}

func ifUpstreamPackageNameApplies(name string) ignoreCondition {
	// with enough ignore rules, we could end up needlessly creating a lot of regexes, which is not ideal.
	// instead lets detect if the input string is a regex or not, and if it is, then compile it...
	// otherwise, we can just do a simple string comparison
	if isLikelyARegex(name) {
		pattern, err := packageNameRegex(name)
		if err != nil {
			log.WithFields("name", name, "error", err).Debug("unable to parse name expression")
			return func(Match) bool { return false }
		}
		return func(match Match) bool {
			for _, upstream := range match.Package.Upstreams {
				if pattern.MatchString(upstream.Name) {
					return true
				}
			}
			return false
		}
	}
	return func(match Match) bool {
		for _, upstream := range match.Package.Upstreams {
			if name == upstream.Name {
				return true
			}
		}
		return false
	}
}

// isRegexPattern is a compiled regex that matches common regex characters. We intentionally leave out
// the '.' character, as it is a common character in package names and versions, and we do not want to
// treat it as a regex unless there is other evidence that it is a regex.
var isRegexPattern = regexp.MustCompile(`[\^\$\*\+\?\[\]\(\)\{\}\|\\]|\\[dDwWsSnrtfv]`)

func isLikelyARegex(s string) bool {
	return isRegexPattern.MatchString(s)
}

func ifMatchTypeApplies(matchType Type) ignoreCondition {
	return func(match Match) bool {
		for _, mType := range match.Details.Types() {
			if mType == matchType {
				return true
			}
		}
		return false
	}
}

func ruleLocationAppliesToMatch(location string, match Match) bool {
	for _, packageLocation := range match.Package.Locations.ToSlice() {
		if ruleLocationAppliesToPath(location, packageLocation.RealPath) {
			return true
		}

		if ruleLocationAppliesToPath(location, packageLocation.AccessPath) {
			return true
		}
	}

	return false
}

func ruleLocationAppliesToPath(location, path string) bool {
	doesMatch, err := doublestar.Match(location, path)
	if err != nil {
		return false
	}

	return doesMatch
}
