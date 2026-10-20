// The CloudFront viewer-request event the Lambda@Edge suites drive the handler
// with. Defined once in scripts/cloudfront-event.mjs, which the Lambda@Edge
// build's smoke invoke uses too, so the event shape cannot drift between the
// suites and the build.
export { viewerRequestEvent as makeViewerRequestEvent } from '../../scripts/cloudfront-event.mjs';
