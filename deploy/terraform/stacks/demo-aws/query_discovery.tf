# Catalog search for the Broker (query_discovery_enabled). The Broker matches an
# agent's free-text query against the catalog feed through a Bedrock model.
#
# The Broker authenticates with an access key of its own IAM user, written to
# the Broker's key directory on the VM. It does not use an instance role: a
# container reaches a role's credentials only with the metadata hop limit
# raised to 2, and that would also let every container read the VM's user
# data, which carries every secret of the stack. Like the SES user, this
# credential travels in the user data, so it can do exactly one thing.

locals {
  query_discovery_feed = coalesce(var.query_discovery_catalog_file, "${path.module}/../../../fixtures/demo/music.jsonl")

  query_discovery = var.query_discovery_enabled ? {
    catalog_feed          = module.query_index_feed[0].index_feed
    model                 = var.query_discovery_model
    aws_region            = var.aws_region
    aws_access_key_id     = aws_iam_access_key.broker_model[0].id
    aws_secret_access_key = aws_iam_access_key.broker_model[0].secret
  } : null
}

# Only when the setting is on, so a stack without catalog search never reads
# the feed file.
module "query_index_feed" {
  count  = var.query_discovery_enabled ? 1 : 0
  source = "../../modules/query-index-feed"

  catalog_feed_path = local.query_discovery_feed
  publisher_host    = local.publisher_fqdn
}

resource "aws_iam_user" "broker_model" {
  count = var.query_discovery_enabled ? 1 : 0
  name  = "${var.name_prefix}-broker-model"
}

resource "aws_iam_user_policy" "broker_model" {
  count = var.query_discovery_enabled ? 1 : 0
  name  = "invoke-query-model"
  user  = aws_iam_user.broker_model[0].name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      # The Converse API is authorized as InvokeModel.
      Effect   = "Allow"
      Action   = "bedrock:InvokeModel"
      Resource = "arn:aws:bedrock:${var.aws_region}::foundation-model/${var.query_discovery_model}"
    }]
  })
}

resource "aws_iam_access_key" "broker_model" {
  count = var.query_discovery_enabled ? 1 : 0
  user  = aws_iam_user.broker_model[0].name
}
