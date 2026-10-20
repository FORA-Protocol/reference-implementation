// viewerRequestEvent builds the CloudFront viewer-request event a Lambda@Edge
// handler is invoked with: GET for the given URL, the Host header CloudFront
// always forwards, plus any extra headers a case needs.
//
// Plain JavaScript so the build script's smoke invoke can import it without a
// compile step; cloudfront-event.d.mts gives the test suites its types. One
// definition, so the event the build smoke-invokes and the event the tests
// drive cannot drift apart.
export function viewerRequestEvent(urlStr, headers = {}) {
  const url = new URL(urlStr);
  const cfHeaders = { host: [{ key: 'Host', value: url.host }] };
  for (const [k, v] of Object.entries(headers)) {
    cfHeaders[k.toLowerCase()] = [{ key: k, value: v }];
  }
  return {
    Records: [
      {
        cf: {
          config: {
            distributionDomainName: 'd.cloudfront.example',
            distributionId: 'EXAMPLE',
            eventType: 'viewer-request',
            requestId: 'req-1',
          },
          request: {
            clientIp: '127.0.0.1',
            method: 'GET',
            uri: url.pathname,
            querystring: url.search.slice(1),
            headers: cfHeaders,
          },
        },
      },
    ],
  };
}
