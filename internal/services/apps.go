// Copyright (c) 2026 Federico Pereira <lord.basex@gmail.com>

package services

// appPolicy decides which app namespaces are accepted. An empty list
// allows every non-empty app.
type appPolicy map[string]struct{}

func newAppPolicy(apps []string) appPolicy {
	if len(apps) == 0 {
		return nil
	}
	p := make(appPolicy, len(apps))
	for _, a := range apps {
		p[a] = struct{}{}
	}
	return p
}

func (p appPolicy) allowed(app string) bool {
	if app == "" {
		return false
	}
	if p == nil {
		return true
	}
	_, ok := p[app]
	return ok
}
