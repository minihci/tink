package remote

// otherAddresses are the addresses a trust token listed besides the one that was kept, when the person did not choose an address
// themselves. A token lists every address the server listens on, and Add keeps the first that answers; saying what else there was lets
// the person see that the choice was made for them, which matters when the kept address is one a router hands out and can change.
func otherAddresses(opts AddOptions, target addTarget, used string) []string {
	if opts.Addr != "" {
		return nil // the person chose: there is nothing to second-guess
	}
	var others []string
	for _, a := range target.addrs {
		if a != used {
			others = append(others, a)
		}
	}
	return others
}
