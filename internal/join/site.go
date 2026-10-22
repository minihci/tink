package join

import (
	"fmt"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/resolve"
)

// A site is the group of stacks whose helpers and publishes the join reads together (resolve/site.go says what it is and how it is declared). Build reads
// whatever it is given as ONE site and requires exactly one dns, ingress and sso helper in it, so a caller that holds more than one site cuts the view down
// to one before it calls Build. This file is that cut, for the two sources of a view:
//
//   - stack files: ForSite, which looks at the user.tink.site stamp that loading a stack with `site:` put on its helpers and published instances;
//   - the server: FromServer and PlatformFromServer take the site, and leave everything of another site unread, a publish stamp included, so a stamp of
//     another site that cannot be read does not stop this one.
//
// An instance belongs to a site only if it says so by a role, a publish or the site stamp itself. The apps that state none of those (an instance with an
// offer that nothing publishes) belong to no site and are in every view, as they were before sites: they are only looked at through a publish.
// The unnamed site is the one with no stamp.

// memberOf says whether an instance with this config is in the view of site. published is whether a publish names it (from a stamp on the server, from a
// kind: publish document in files).
func memberOf(cfg map[string]string, published bool, site string) bool {
	have := strings.TrimSpace(cfg[resolve.SiteKey])
	if have == site {
		return true
	}
	if have == "" && strings.TrimSpace(cfg[resolve.RoleKey]) == "" && !published {
		return true // it states no site, no role and no publish: not a part of any site, so not left out of any
	}
	return false
}

// hasPublishStamp says whether an instance's config carries a publish stamp.
func hasPublishStamp(cfg map[string]string) bool {
	for k := range cfg {
		if strings.HasPrefix(k, resolve.PublishStampPrefix) {
			return true
		}
	}
	return false
}

// ForSite cuts the stacks down to one site: the helpers and the published instances of that site, with their publishes, and everything that is not part
// of any site. Stacks that name no site are the unnamed site, "". The stacks it is given are not changed.
//
// A publish is in the site of the instance it names, the same rule the server's stamps follow (the stamp is on the instance). The instance is looked for in
// the publish's own stack first, and then in the others (the join reads publishes across stacks). One whose instance is not in the files at all stays, so
// that Build reports the missing instance and not a silence.
func ForSite(stacks []Stack, site string) []Stack {
	type loc struct{ project, name string }
	published := map[loc]bool{}
	config := map[loc]map[string]string{} // any stack's instance of that name, for a publish whose own stack lacks it
	own := make([]map[loc]map[string]string, len(stacks))
	for i, s := range stacks {
		own[i] = map[loc]map[string]string{}
		for _, r := range s.Resources {
			switch r.Kind {
			case resolve.KindPublish:
				published[loc{projectOf(r), r.Instance}] = true
			case resolve.KindInstance:
				config[loc{projectOf(r), r.Name}] = r.Config
				own[i][loc{projectOf(r), r.Name}] = r.Config
			}
		}
	}
	out := make([]Stack, 0, len(stacks))
	for i, s := range stacks {
		kept := Stack{Name: s.Name, Elsewhere: copyCounts(s.Elsewhere)}
		for _, r := range s.Resources {
			switch r.Kind {
			case resolve.KindInstance:
				if !memberOf(r.Config, published[loc{projectOf(r), r.Name}], site) {
					if strings.TrimSpace(r.Config[resolve.RoleKey]) != "" {
						if kept.Elsewhere == nil {
							kept.Elsewhere = map[string]int{}
						}
						kept.Elsewhere[strings.TrimSpace(r.Config[resolve.SiteKey])]++
					}
					continue
				}
			case resolve.KindPublish:
				cfg, ok := own[i][loc{projectOf(r), r.Instance}]
				if !ok {
					cfg, ok = config[loc{projectOf(r), r.Instance}]
				}
				if ok && strings.TrimSpace(cfg[resolve.SiteKey]) != site {
					continue
				}
			}
			kept.Resources = append(kept.Resources, r)
		}
		out = append(out, kept)
	}
	return out
}

// SitesOf are the site names the stacks' helpers are in, sorted, with "" (the unnamed site) as "". It is what a caller that was given files and no site
// uses to find the one they are all in.
func SitesOf(stacks []Stack) []string {
	seen := map[string]bool{}
	for _, s := range stacks {
		for _, r := range s.Resources {
			if r.Kind == resolve.KindInstance && (strings.TrimSpace(r.Config[resolve.RoleKey]) != "" || strings.TrimSpace(r.Config[resolve.SiteKey]) != "") {
				seen[strings.TrimSpace(r.Config[resolve.SiteKey])] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// elsewhereNote says, when a view lacks or doubles a helper, that helpers of other sites were left out of it. A server with several sites is the usual
// reason a site seems to have no dns helper, and "none found" would send a person looking for something that is there.
func elsewhereNote(stacks []Stack) string {
	total := map[string]int{}
	for _, s := range stacks {
		for site, n := range s.Elsewhere {
			total[site] += n
		}
	}
	if len(total) == 0 {
		return ""
	}
	names := make([]string, 0, len(total))
	for site := range total {
		names = append(names, site)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, site := range names {
		label := fmt.Sprintf("site %q", site)
		if site == "" {
			label = "the unnamed site"
		}
		parts = append(parts, fmt.Sprintf("%s (%d helper(s))", label, total[site]))
	}
	return "helpers in other sites were left out of this view: " + strings.Join(parts, ", ") + "; --site NAME reads another site"
}

func copyCounts(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// NoDNSHelper says why the result has no dns helper to write to: none was found, or more than one. A message that called both "none found" sent people to
// look for a helper that was there.
func (r Result) NoDNSHelper() string {
	if len(r.DNSHelpers) > 1 {
		return fmt.Sprintf("%d dns helpers were found (%s), and a site has exactly one: give each its own site (site: on the stack that declares it)", len(r.DNSHelpers), strings.Join(r.DNSHelpers, ", "))
	}
	return fmt.Sprintf("no dns helper was found (no instance in this site has %s: dns)", roleKey)
}
