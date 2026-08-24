// Package ext holds optional sandbox.Pack implementations that are
// generically useful but don't belong in the core sandbox package.
//
// sandbox's built-in packs (require, http, fetch, markdown, ai, help,
// skills) are the minimal set every agentloop deployment needs. The
// packs here — email, secret, document search, a document store — are
// common but optional: most of the applications this was extracted
// from use two or three of them, never all four, and a new deployment
// starts with none.
//
// Each pack follows the same decoupling the core packs use: it takes a
// callback or a small interface (EmailPack's send func, SecretPack's
// get func, SearchPack's search func, StoresPack's StoresBackend)
// rather than a concrete dependency, so this package stays free of any
// mail/secrets/search-backend import. An application wires its own
// backend in when it builds the agentloop.Capability that installs the
// pack — see the package examples and DefaultCapabilities in the
// agentloop package for the pattern.
package ext
