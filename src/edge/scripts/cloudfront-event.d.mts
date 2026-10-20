import type { CloudFrontEdgeEvent } from 'hono/lambda-edge';

// Type declaration for the plain-JS cloudfront-event.mjs, which the build
// script imports without a compile step.
export function viewerRequestEvent(
  urlStr: string,
  headers?: Record<string, string>,
): CloudFrontEdgeEvent;
