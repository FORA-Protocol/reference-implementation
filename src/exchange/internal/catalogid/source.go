package catalogid

// SourceHeader is the request header a catalog push names its source in. The
// automatic import sends it on every push it makes, with SourceAutoImport as
// the value; fora-ingest, the manual path, never sends it.
//
// It is a label, not an identity. The push's signature does not cover it, and
// any contributor a site authorizes may send it on its own push. The Exchange
// reads it for one purpose only: to record, on each row a push writes, whether
// the automatic import may refresh that row later. A push without it -- or
// with any other value -- leaves its rows to their owner.
const SourceHeader = "X-Fora-Catalog-Source"

// SourceAutoImport is the SourceHeader value the automatic import sends.
const SourceAutoImport = "auto-import"
