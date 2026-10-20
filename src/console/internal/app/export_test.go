package app

// PublisherPagePatterns lists the patterns of every page that needs a signed-in
// person whose address is verified, and WizardRoutePatterns those of the
// wizard's JSON routes, for the access tests that walk every one of them.
func PublisherPagePatterns() []string { return patterns(publisherPages) }

// WizardRoutePatterns lists the patterns of the wizard's JSON routes.
func WizardRoutePatterns() []string { return patterns(wizardRoutes) }

func patterns(routes []route) []string {
	out := make([]string, 0, len(routes))
	for _, rt := range routes {
		out = append(out, rt.pattern)
	}
	return out
}
