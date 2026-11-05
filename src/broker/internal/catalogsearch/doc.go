// Package catalogsearch is the port between the Broker's resolve core and its
// catalog search providers: the shapes a provider implements, and nothing else.
//
// A provider is two things. It is a Vocabulary, which decides before any search
// runs which search_filters keys it applies and whether each value is well
// formed. And it is a Searcher, which turns a query and the filters its own
// Vocabulary parsed into catalog URIs. The resolve core owns the order of the
// checks and the mapping of a refusal to a Broker error; a provider owns only
// what its vocabulary means.
//
// The package imports only the standard library, so a provider and the resolve
// core can both depend on it without depending on each other.
package catalogsearch
