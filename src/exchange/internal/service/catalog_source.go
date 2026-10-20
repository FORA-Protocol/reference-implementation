package service

import (
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// PushSource is which client made a catalog push, as far as the Exchange keeps
// track: the automatic import, or anyone else. The transport reads it off the
// request -- this package never sees HTTP -- and the push records it on every
// row it stores.
//
// It is a label the pusher chooses, not an identity, and it authorizes
// nothing. Its one effect is on which rows the automatic import may refresh
// later. The zero value is PushSourceOther, so a caller that names no source
// writes rows the import leaves to their owner.
type PushSource int

const (
	// PushSourceOther is every push that does not declare itself the
	// automatic import: fora-ingest, a publisher's own tooling, a test.
	PushSourceOther PushSource = iota
	// PushSourceAutoImport is a push the automatic import made.
	PushSourceAutoImport
)

// stampSource records the push's source on each accepted entry: the rows a
// push writes are the automatic import's exactly when the push was. The last
// push of a row decides, so any other push hands a row the import wrote back
// to whoever pushed it.
func stampSource(accepted []repo.CatalogEntry, source PushSource) {
	for i := range accepted {
		accepted[i].AutoImported = source == PushSourceAutoImport
	}
}
