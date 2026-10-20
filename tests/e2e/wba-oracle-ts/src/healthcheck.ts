// Container healthcheck: exits 0 once the oracle answers GET /healthz.
//
// A script rather than curl because the slim Node image ships no curl, and the
// one runtime this image is guaranteed to have is Node.

try {
  const response = await fetch('http://127.0.0.1:8090/healthz');
  process.exit(response.ok ? 0 : 1);
} catch {
  process.exit(1);
}
