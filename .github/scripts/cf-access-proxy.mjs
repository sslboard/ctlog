// Local reverse proxy that adds Cloudflare Access service token headers.
// The SonarQube scanner cannot send custom headers, so CI points it at this
// proxy, which forwards to the real SonarQube host behind Cloudflare Access.
import http from 'node:http';
import https from 'node:https';

const upstream = new URL(process.env.UPSTREAM_URL ?? '');
const port = Number(process.env.PROXY_PORT ?? 9000);
const id = process.env.CF_ACCESS_CLIENT_ID;
const secret = process.env.CF_ACCESS_CLIENT_SECRET;
if (!id || !secret) {
  console.error('Missing CF_ACCESS_CLIENT_ID or CF_ACCESS_CLIENT_SECRET');
  process.exit(1);
}

const basePath = upstream.pathname.replace(/\/$/, '');
const localOrigin = `http://127.0.0.1:${port}`;

http
  .createServer((req, res) => {
    const headers = {
      ...req.headers,
      host: upstream.host,
      'cf-access-client-id': id,
      'cf-access-client-secret': secret,
    };
    const upstreamReq = https.request(
      {
        hostname: upstream.hostname,
        port: upstream.port || 443,
        path: basePath + req.url,
        method: req.method,
        headers,
      },
      (upstreamRes) => {
        const outHeaders = { ...upstreamRes.headers };
        if (outHeaders.location) {
          outHeaders.location = outHeaders.location.replace(upstream.origin + basePath, localOrigin);
        }
        res.writeHead(upstreamRes.statusCode ?? 502, outHeaders);
        upstreamRes.pipe(res);
      },
    );
    upstreamReq.on('error', (err) => {
      console.error(`Proxy error: ${err.message}`);
      if (!res.headersSent) res.writeHead(502);
      res.end();
    });
    req.pipe(upstreamReq);
  })
  .listen(port, '127.0.0.1', () => console.log(`Cloudflare Access proxy listening on ${localOrigin}`));
