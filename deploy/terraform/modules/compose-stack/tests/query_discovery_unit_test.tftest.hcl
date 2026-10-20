# Rendering tests for the optional Broker catalog search (query_discovery).
#
# Same properties as its siblings: `command = apply` is needed because the
# templates interpolate random_password, and the module's only resources ARE
# those in-memory random strings — nothing is created outside the test process.
#
# Run from the module directory: terraform init && terraform test

variables {
  exchange_hostname = "exchange.staging.example"
  broker_hostname   = "broker.staging.example"
  identity_hostname = "mcp.staging.example"
  zitadel_hostname  = "login.staging.example"

  default_tenant_domain = "exchange.staging.example"

  exchange_image = "registry.example.com/group/proj/exchange:test"
  broker_image   = "registry.example.com/group/proj/broker:test"
  identity_image = "registry.example.com/group/proj/identity:test"

  acme_email = "ops@example.com"

  # Dummy key material — shape only, never used cryptographically.
  ed25519_private_pem     = "-----BEGIN PRIVATE KEY-----\ndGVzdA==\n-----END PRIVATE KEY-----\n"
  broker_relay_key_json   = "{\"kid\":\"broker.test.v1\"}"
  broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"
}

run "query_discovery_is_off_by_default" {
  command = apply

  assert {
    condition     = !strcontains(output.compose_yaml, "BROKER_QUERY_") && !strcontains(output.compose_yaml, "AWS_")
    error_message = "With no query_discovery input the Broker must get no catalog search or AWS settings — the existing behaviour is unchanged."
  }

  assert {
    condition     = !strcontains(output.user_data, "/opt/fora/catalog/") && !strcontains(output.user_data, "aws-credentials")
    error_message = "With no query_discovery input cloud-init must write no catalog feed and no credentials file."
  }
}

run "query_discovery_renders_when_configured" {
  command = apply

  variables {
    query_discovery = {
      catalog_feed          = "{\"domain\":\"music.example\",\"path\":\"/a.mp3\",\"terms\":[]}\n"
      model                 = "amazon.nova-lite-v1:0"
      aws_region            = "us-east-1"
      aws_access_key_id     = "AKIATESTKEYID"
      aws_secret_access_key = "test-secret-access-key"
    }
  }

  assert {
    condition = alltrue([for line in [
      "BROKER_QUERY_DISCOVERY: \"on\"",
      "BROKER_QUERY_CATALOG_FILE: \"/catalog/feed.jsonl\"",
      "BROKER_QUERY_MODEL: \"amazon.nova-lite-v1:0\"",
      "AWS_REGION: \"us-east-1\"",
      "AWS_SHARED_CREDENTIALS_FILE: \"/keys/aws-credentials\"",
      "AWS_EC2_METADATA_DISABLED: \"true\"",
      "- /opt/fora/catalog/feed.jsonl:/catalog/feed.jsonl:ro",
    ] : strcontains(output.compose_yaml, line)])
    error_message = "The Broker must get the catalog search settings, the credentials file path and the read-only feed mount."
  }

  # The same settings read back from the parsed documents, so a template edit
  # that breaks the YAML structure fails here even where the text still matches.
  assert {
    condition = (
      yamldecode(output.compose_yaml).services.broker.environment.BROKER_QUERY_DISCOVERY == "on" &&
      contains(yamldecode(output.compose_yaml).services.broker.volumes, "/opt/fora/catalog/feed.jsonl:/catalog/feed.jsonl:ro") &&
      contains([for f in yamldecode(output.user_data).write_files : f.path], "/opt/fora/keys/broker/aws-credentials")
    )
    error_message = "The rendered compose file and cloud-init must parse, with the settings on the Broker service and the credentials file in write_files."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "test-secret-access-key") && !strcontains(output.compose_yaml, "AKIATESTKEYID")
    error_message = "The key pair must not appear in the compose file, which every service's environment is rendered from; it belongs in the Broker's key directory only."
  }

  assert {
    condition     = strcontains(output.user_data, base64encode("{\"domain\":\"music.example\",\"path\":\"/a.mp3\",\"terms\":[]}\n"))
    error_message = "cloud-init must write the catalog feed byte for byte."
  }

  assert {
    condition = strcontains(output.user_data, "- path: /opt/fora/keys/broker/aws-credentials") && strcontains(output.user_data,
    base64encode("[default]\naws_access_key_id = AKIATESTKEYID\naws_secret_access_key = test-secret-access-key\n"))
    error_message = "cloud-init must write the key pair as a shared-credentials file in the Broker's own key directory."
  }
}

run "query_discovery_rejects_an_empty_setting" {
  command = plan

  variables {
    query_discovery = {
      catalog_feed          = "{}"
      model                 = " "
      aws_region            = "us-east-1"
      aws_access_key_id     = "AKIATESTKEYID"
      aws_secret_access_key = "test-secret-access-key"
    }
  }

  expect_failures = [var.query_discovery]
}

# The comment-free user data a stack uses when the commented one would exceed
# EC2's 16 KB limit (demo-aws with catalog search on). Rendered with catalog
# search on, so the feed and the key file are covered too.
run "user_data_without_comments_keeps_every_setting" {
  command = apply

  variables {
    query_discovery = {
      catalog_feed          = "{\"domain\":\"music.example\",\"path\":\"/a.mp3\",\"terms\":[]}\n"
      model                 = "amazon.nova-lite-v1:0"
      aws_region            = "us-east-1"
      aws_access_key_id     = "AKIATESTKEYID"
      aws_secret_access_key = "test-secret-access-key"
    }
  }

  assert {
    condition = (
      startswith(output.user_data_without_comments, "#cloud-config\n") &&
      length(regexall("(?m)^[ \t]*#", output.user_data_without_comments)) == 1
    )
    error_message = "The only comment-like line left must be the #cloud-config header, which cloud-init needs on the first line."
  }

  # Everything but the comments survives: the same top-level settings, the
  # same files in the same order, every file byte for byte except the three
  # embedded documents, whose parsed content (or, for the Caddyfile, every
  # non-comment line) is unchanged.
  assert {
    condition = (
      { for k, v in yamldecode(output.user_data_without_comments) : k => v if k != "write_files" } ==
      { for k, v in yamldecode(output.user_data) : k => v if k != "write_files" } &&
      [for f in yamldecode(output.user_data_without_comments).write_files : f.path] ==
      [for f in yamldecode(output.user_data).write_files : f.path] &&
      alltrue([for i, f in yamldecode(output.user_data_without_comments).write_files : (
        contains(["/opt/fora/docker-compose.yml", "/opt/fora/zitadel/init-steps.yaml"], f.path)
        ? yamldecode(f.content) == yamldecode(yamldecode(output.user_data).write_files[i].content)
        : f.path == "/opt/fora/Caddyfile"
        ? f.content == replace(yamldecode(output.user_data).write_files[i].content, "/(?m)^[ \\t]*#.*\\n/", "")
        : f == yamldecode(output.user_data).write_files[i]
      )])
    )
    error_message = "Removing the comments must not change any setting, file, or file content."
  }
}
