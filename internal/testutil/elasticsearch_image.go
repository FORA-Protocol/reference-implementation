package testutil

// ElasticsearchImage is the node the integration tests run against, and the
// version the Broker documents as tested. It has no build tag because the guard
// that keeps the e2e stack and the operator documentation on the same version
// reads it outside the integration tier.
const ElasticsearchImage = "docker.elastic.co/elasticsearch/elasticsearch:7.15.0"
