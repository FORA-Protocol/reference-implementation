# Unit tests for the reduced catalog feed. No providers: the module only reads
# and reshapes a file.
#
# Run from the module directory: terraform init && terraform test

variables {
  catalog_feed_path = "tests/fixture.jsonl"
  publisher_host    = "demo.demo.example"
}

run "keeps_only_the_index_fields_of_tagged_records" {
  command = plan

  assert {
    condition = [for line in compact(split("\n", output.index_feed)) : jsondecode(line).path] == [
      "/tracks/a.mp3", "/sfx/b.wav", "/c.mp3", "/d.mp3",
    ]
    error_message = "Only records with a music.<field> key belong in the index, in feed order; a blank line is skipped."
  }

  assert {
    condition = alltrue([
      for line in compact(split("\n", output.index_feed)) :
      keys(jsondecode(line)) == ["domain", "ext", "path", "terms"] && jsondecode(line).terms == [] && alltrue([for k in keys(jsondecode(line).ext) : startswith(k, "music.")])
    ])
    error_message = "Each record carries domain, path, empty terms and its music.<field> keys only."
  }

  assert {
    condition     = jsondecode(split("\n", output.index_feed)[0]).ext == { "music.genre" = "soundtrack", "music.bpm" = 85 }
    error_message = "The music.<field> keys must arrive unchanged, and other ext keys must be dropped."
  }
}

# scripts/seed-staging.sh rewrites every demo brand host to the publisher host
# before ingesting; the index must name the URIs the Exchange stores.
run "rewrites_demo_hosts_like_the_seed" {
  command = plan

  assert {
    condition = [for line in compact(split("\n", output.index_feed)) : jsondecode(line).domain] == [
      "demo.demo.example", "demo.demo.example", "demo.demo.example", "music.example",
    ]
    error_message = "music., sfx. and bare demo.fora-protocol.org become the publisher host; any other host is kept."
  }
}

run "the_demo_music_feed_indexes_every_track" {
  command = plan

  variables {
    catalog_feed_path = "../../../fixtures/demo/music.jsonl"
  }

  assert {
    condition     = length(compact(split("\n", output.index_feed))) == 10
    error_message = "The demo music feed has ten tagged tracks."
  }
}
