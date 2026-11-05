# Rendering tests for the optional Elasticsearch catalog search
# (elasticsearch_search).
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

run "elasticsearch_search_is_off_by_default" {
  command = apply

  assert {
    condition     = !strcontains(output.compose_yaml, "BROKER_QUERY_") && !strcontains(output.compose_yaml, "BROKER_ES_")
    error_message = "With no catalog search input the Broker must get no provider setting: its default is none."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "IDENTITY_MCP_SEARCH_FILTERS_SCHEMA") && !strcontains(output.compose_yaml, "es-search-filters.json")
    error_message = "With no elasticsearch_search input the Identity Service must get no filter schema: neither the setting nor the file."
  }

  assert {
    condition = alltrue([
      for name in ["es-api-key", "es-ca.pem", "es-search-template.mustache", "es-search-filters.json"] : !strcontains(output.user_data, name)
    ])
    error_message = "With no elasticsearch_search input cloud-init must write none of the search's files."
  }
}

# A template stored in the cluster: the Broker names it by its id, and there is
# no template file. With a private CA and no filter schema.
run "elasticsearch_search_renders_with_a_stored_template" {
  command = apply

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example:9243"
      index        = "catalog-current"
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 50
      api_key      = "dGVzdC1rZXk6dGVzdA=="
      ca_pem       = "-----BEGIN CERTIFICATE-----\nZmFrZSBjYQ==\n-----END CERTIFICATE-----\n"
    }
  }

  assert {
    condition = alltrue([for line in [
      "BROKER_QUERY_PROVIDER: \"elasticsearch\"",
      "BROKER_ES_URL: \"https://search.staging.example:9243\"",
      "BROKER_ES_INDEX: \"catalog-current\"",
      "BROKER_ES_TEMPLATE_ID: \"fora-catalog\"",
      "BROKER_ES_HIT_URL_PATH: \"_source.url\"",
      "BROKER_ES_RESULT_LIMIT: \"50\"",
      "BROKER_ES_TIMEOUT: \"3s\"",
      "BROKER_ES_AUTH: \"api_key\"",
      "BROKER_ES_API_KEY_FILE: \"/keys/es-api-key\"",
      "BROKER_ES_CA_FILE: \"/keys/es-ca.pem\"",
    ] : strcontains(output.compose_yaml, line)])
    error_message = "The Broker must get every Elasticsearch setting, the stored template's id, the default timeout, the API-key mode and the two file paths."
  }

  # The same settings read back from the parsed documents, so a template edit
  # that breaks the YAML structure fails here even where the text still matches.
  assert {
    condition = (
      yamldecode(output.compose_yaml).services.broker.environment.BROKER_QUERY_PROVIDER == "elasticsearch" &&
      yamldecode(output.compose_yaml).services.broker.environment.BROKER_ES_TEMPLATE_ID == "fora-catalog" &&
      yamldecode(output.compose_yaml).services.broker.environment.BROKER_ES_RESULT_LIMIT == "50" &&
      contains(yamldecode(output.compose_yaml).services.broker.volumes, "/opt/fora/keys/broker:/keys:ro")
    )
    error_message = "The rendered compose file must parse, with the settings on the Broker service and its key directory mounted."
  }

  # A stored template is no file. Without a schema there is no schema file and
  # nothing for the Identity Service. The plain-http opt-in, which this module
  # never sets, and the other provider's settings stay out too.
  assert {
    condition = alltrue([for absent in [
      "BROKER_ES_TEMPLATE_FILE",
      "BROKER_ES_FILTERS_SCHEMA_FILE",
      "IDENTITY_MCP_SEARCH_FILTERS_SCHEMA_FILE",
      "es-search-filters.json",
      "BROKER_ES_ALLOW_HTTP",
      "BROKER_QUERY_CATALOG_FILE",
      "AWS_",
    ] : !strcontains(output.compose_yaml, absent)])
    error_message = "With a stored template and no schema the compose file must name no template file and no schema, on the Broker or the Identity Service, and none of the settings this provider does not use."
  }

  assert {
    condition     = !strcontains(output.user_data, "es-search-template.mustache") && !strcontains(output.user_data, "es-search-filters.json")
    error_message = "cloud-init must write no template file for a stored template, and no schema file without a schema."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "dGVzdC1rZXk6dGVzdA==") && !strcontains(output.compose_yaml, "ZmFrZSBjYQ==")
    error_message = "The key and the CA must not appear in the compose file, which every service's environment is rendered from; they belong in the Broker's key directory only."
  }

  assert {
    condition = alltrue([
      for f in yamldecode(output.user_data).write_files : f.path != "/opt/fora/keys/broker/es-api-key" || (
        f.permissions == "0600" && f.encoding == "b64" && f.content == base64encode("dGVzdC1rZXk6dGVzdA==")
      )
    ]) && contains([for f in yamldecode(output.user_data).write_files : f.path], "/opt/fora/keys/broker/es-api-key")
    error_message = "cloud-init must write the API key byte for byte, readable by root alone until runcmd hands it to the Broker."
  }

  assert {
    condition = alltrue([
      for f in yamldecode(output.user_data).write_files : f.path != "/opt/fora/keys/broker/es-ca.pem" || (
        f.permissions == "0644" && f.encoding == "b64" &&
        f.content == base64encode("-----BEGIN CERTIFICATE-----\nZmFrZSBjYQ==\n-----END CERTIFICATE-----\n")
      )
    ]) && contains([for f in yamldecode(output.user_data).write_files : f.path], "/opt/fora/keys/broker/es-ca.pem")
    error_message = "cloud-init must write the CA byte for byte."
  }
}

# The template's text, which the Broker sends with every search, and a filter
# schema: both reach the Broker as files in its key directory, and the schema
# reaches the Identity Service as well. Without a CA the Broker trusts the
# system roots.
run "elasticsearch_search_renders_with_a_template_file_and_a_schema" {
  command = apply

  variables {
    elasticsearch_search = {
      url            = "https://search.staging.example"
      index          = "catalog"
      template       = "{\"query\": {\"multi_match\": {\"query\": \"{{query}}\", \"fields\": [\"title^3\", \"body\"]}}, \"size\": \"{{limit}}\"}\n"
      hit_url_path   = "fields.url.0"
      result_limit   = 10
      timeout        = "750ms"
      api_key        = "dGVzdC1rZXk6dGVzdA=="
      filters_schema = "{\"type\": \"object\", \"additionalProperties\": false, \"properties\": {\"news.language\": {\"type\": \"string\", \"pattern\": \"^[a-z]{2}$\"}}}\n"
    }
  }

  assert {
    condition = alltrue([for line in [
      "BROKER_ES_TEMPLATE_FILE: \"/keys/es-search-template.mustache\"",
      "BROKER_ES_HIT_URL_PATH: \"fields.url.0\"",
      "BROKER_ES_TIMEOUT: \"750ms\"",
      "BROKER_ES_FILTERS_SCHEMA_FILE: \"/keys/es-search-filters.json\"",
    ] : strcontains(output.compose_yaml, line)])
    error_message = "The Broker must read the template and the schema from its key directory, with the configured timeout."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "BROKER_ES_TEMPLATE_ID") && !strcontains(output.compose_yaml, "BROKER_ES_CA_FILE")
    error_message = "A template sent with every search has no stored id, and without a CA there is no CA setting."
  }

  # The Identity Service gets the setting and the Broker's own schema file,
  # mounted read-only on its own: no other path from the Broker's key directory.
  assert {
    condition = (
      yamldecode(output.compose_yaml).services.identity.environment.IDENTITY_MCP_SEARCH_FILTERS_SCHEMA_FILE == "/search-filters/es-search-filters.json" &&
      contains(yamldecode(output.compose_yaml).services.identity.volumes, "/opt/fora/keys/broker/es-search-filters.json:/search-filters/es-search-filters.json:ro") &&
      length([for v in yamldecode(output.compose_yaml).services.identity.volumes : v if strcontains(v, "/opt/fora/keys/")]) == 1
    )
    error_message = "With a filter schema the Identity Service must name the schema file and mount it read-only, and nothing else of the Broker's key directory."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "multi_match") && !strcontains(output.compose_yaml, "news.language") && !strcontains(output.compose_yaml, "dGVzdC1rZXk6dGVzdA==")
    error_message = "The template, the schema and the key must not appear in the compose file: they reach the services as files only."
  }

  assert {
    condition = alltrue([
      for f in yamldecode(output.user_data).write_files : f.path != "/opt/fora/keys/broker/es-search-template.mustache" || (
        f.permissions == "0644" && f.encoding == "b64" && f.content == base64encode(var.elasticsearch_search.template)
      )
    ]) && contains([for f in yamldecode(output.user_data).write_files : f.path], "/opt/fora/keys/broker/es-search-template.mustache")
    error_message = "cloud-init must write the template byte for byte, readable by the Broker once runcmd re-owns the key directory."
  }

  assert {
    condition = alltrue([
      for f in yamldecode(output.user_data).write_files : f.path != "/opt/fora/keys/broker/es-search-filters.json" || (
        f.permissions == "0644" && f.encoding == "b64" && f.content == base64encode(var.elasticsearch_search.filters_schema)
      )
    ]) && contains([for f in yamldecode(output.user_data).write_files : f.path], "/opt/fora/keys/broker/es-search-filters.json")
    error_message = "cloud-init must write the schema byte for byte, readable by the Broker and the Identity Service once runcmd re-owns the key directory."
  }

  assert {
    condition     = contains([for f in yamldecode(output.user_data).write_files : f.path], "/opt/fora/keys/broker/es-api-key") && !strcontains(output.user_data, "es-ca.pem")
    error_message = "cloud-init must write the API key, and no CA file without a CA."
  }
}

run "elasticsearch_search_rejects_an_empty_setting" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = " "
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_an_empty_hit_url_path" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      hit_url_path = ""
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_plain_http" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "http://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_a_zero_limit" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 0
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_a_limit_above_the_protocol_cap" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 257
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_a_fractional_limit" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 10.5
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_both_template_forms" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      template     = "{\"query\": {\"match_all\": {}}}"
      hit_url_path = "_source.url"
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_no_template" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      hit_url_path = "_source.url"
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_a_blank_template_id" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = " "
      hit_url_path = "_source.url"
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_a_schema_without_additional_properties" {
  command = plan

  variables {
    elasticsearch_search = {
      url            = "https://search.staging.example"
      index          = "catalog"
      template_id    = "fora-catalog"
      hit_url_path   = "_source.url"
      result_limit   = 10
      api_key        = "dGVzdC1rZXk6dGVzdA=="
      filters_schema = "{\"type\": \"object\", \"properties\": {\"news.language\": {\"type\": \"string\"}}}"
    }
  }

  expect_failures = [var.elasticsearch_search]
}

# Present but true lets an undeclared key through, which the Broker would then
# ignore rather than refuse.
run "elasticsearch_search_rejects_a_schema_that_admits_undeclared_keys" {
  command = plan

  variables {
    elasticsearch_search = {
      url            = "https://search.staging.example"
      index          = "catalog"
      template_id    = "fora-catalog"
      hit_url_path   = "_source.url"
      result_limit   = 10
      api_key        = "dGVzdC1rZXk6dGVzdA=="
      filters_schema = "{\"type\": \"object\", \"additionalProperties\": true, \"properties\": {\"news.language\": {\"type\": \"string\"}}}"
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "elasticsearch_search_rejects_a_ca_that_is_not_pem" {
  command = plan

  variables {
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
      ca_pem       = "not a certificate"
    }
  }

  expect_failures = [var.elasticsearch_search]
}

run "the_two_catalog_searches_exclude_each_other" {
  command = plan

  variables {
    query_discovery = {
      catalog_feed          = "{\"domain\":\"music.example\",\"path\":\"/a.mp3\",\"terms\":[]}\n"
      model                 = "amazon.nova-lite-v1:0"
      aws_region            = "us-east-1"
      aws_access_key_id     = "AKIATESTKEYID"
      aws_secret_access_key = "test-secret-access-key"
    }
    elasticsearch_search = {
      url          = "https://search.staging.example"
      index        = "catalog"
      template_id  = "fora-catalog"
      hit_url_path = "_source.url"
      result_limit = 10
      api_key      = "dGVzdC1rZXk6dGVzdA=="
    }
  }

  expect_failures = [random_password.postgres]
}
