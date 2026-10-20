# fora-music-v1 JSON Schema

`music-v1.schema.json` is a JSON Schema (Draft 2020-12) for the fora-music-v1
extension profile: the flat `music.<field>` keys in the `ext` of a music catalog
feed record, which the Exchange copies unchanged into `Offer.ext` before it
signs the offer. The schema describes the whole `ext` object and allows every
other key.

Every field is optional. An absent field means unknown, an empty array means
known to be empty, and `null` is not a valid value. Unknown fields are allowed,
and consumers ignore them.

`internal/catalogfeed/music_schema_test.go` validates against this schema the
`ext` of every record with a `music.<field>` key in the music feeds this
repository ships, and checks that the schema refuses the shapes the profile
forbids.
