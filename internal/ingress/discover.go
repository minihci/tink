package ingress

import (
	"fmt"
	"sort"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// Registration is one instance that opted in to ingress self-registration
// via user.ingress.{domain,port,enabled}, with its current address
// resolved from live instance state rather than stored -- this is what
// makes a DHCP-leased instance safe to register: the address is
// re-resolved every pass, so a lease change heals on the next poll.
type Registration struct {
	Name    string
	Domain  string
	Port    string
	Address string
}

// Discover queries every instance on the daemon and delegates to
// filterAndResolve for the actual (independently testable) logic.
func Discover(server incus.InstanceServer) (regs []Registration, warnings []string, err error) {
	instances, err := server.GetInstancesFull(api.InstanceTypeAny)
	if err != nil {
		return nil, nil, fmt.Errorf("listing instances: %w", err)
	}
	regs, warnings = filterAndResolve(instances)
	return regs, warnings, nil
}

// filterAndResolve filters to instances that opted in, resolves each
// one's current address, and reports (as warnings, not errors) any domain
// conflicts or instances with no address yet -- mirroring reconcile.sh's
// discovery pass, including its "log and skip every claimant" conflict
// handling: a contested domain never gets a silently picked winner.
func filterAndResolve(instances []api.InstanceFull) (regs []Registration, warnings []string) {
	type candidate struct {
		name, domain, port string
		hasAddress         bool
		address            string
	}

	var candidates []candidate
	for _, inst := range instances {
		if inst.Config["user.ingress.enabled"] != "true" {
			continue
		}
		domain := inst.Config["user.ingress.domain"]
		if domain == "" {
			continue
		}
		port := inst.Config["user.ingress.port"]
		if port == "" {
			port = "80"
		}

		address, ok := firstInetAddress(inst)
		candidates = append(candidates, candidate{
			name:       inst.Name,
			domain:     domain,
			port:       port,
			hasAddress: ok,
			address:    address,
		})
	}

	byDomain := map[string][]candidate{}
	for _, c := range candidates {
		byDomain[c.domain] = append(byDomain[c.domain], c)
	}

	domains := make([]string, 0, len(byDomain))
	for d := range byDomain {
		domains = append(domains, d)
	}
	sort.Strings(domains)

	for _, domain := range domains {
		claimants := byDomain[domain]
		if len(claimants) > 1 {
			warnings = append(warnings, fmt.Sprintf("domain %q claimed by multiple instances, skipping all of them", domain))
			continue
		}

		c := claimants[0]
		if !c.hasAddress {
			warnings = append(warnings, fmt.Sprintf("%s has no address yet (not started?), skipping this pass", c.name))
			continue
		}

		regs = append(regs, Registration{
			Name:    c.name,
			Domain:  c.domain,
			Port:    c.port,
			Address: c.address,
		})
	}

	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })

	return regs, warnings
}

func firstInetAddress(inst api.InstanceFull) (string, bool) {
	if inst.State == nil {
		return "", false
	}
	eth0, ok := inst.State.Network["eth0"]
	if !ok {
		return "", false
	}
	for _, addr := range eth0.Addresses {
		if addr.Family == "inet" {
			return addr.Address, true
		}
	}
	return "", false
}
