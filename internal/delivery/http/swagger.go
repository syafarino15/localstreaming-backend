package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	docs "github.com/alfateve/stayin-local-backend/docs"
)

// swaggerIndex is a self-contained API reference. It deliberately avoids a CDN:
// a ship node must render this page with no internet access at all.
const swaggerIndex = `<!doctype html>
<html lang="id">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>StayIn Local Side — API</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin:0; font:14px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
         background:#0b1220; color:#dbe4f0; }
  header { padding:24px 28px; border-bottom:1px solid #1e2b42; background:#0f1a2e; }
  header h1 { margin:0 0 6px; font-size:20px; color:#f2f6fc; }
  header p { margin:0; color:#8fa4c0; max-width:900px; }
  main { display:grid; grid-template-columns:320px 1fr; min-height:calc(100vh - 96px); }
  nav { border-right:1px solid #1e2b42; padding:16px; overflow:auto; max-height:calc(100vh - 96px); }
  nav a { display:block; color:#9ec1e8; text-decoration:none; padding:4px 6px; border-radius:4px;
          white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
  nav a:hover { background:#16233a; color:#fff; }
  nav .m { display:inline-block; width:52px; color:#59d08a; font-weight:700; }
  section { padding:20px 28px; overflow:auto; max-height:calc(100vh - 96px); }
  pre { background:#0f1a2e; border:1px solid #1e2b42; border-radius:8px; padding:16px;
        overflow:auto; font-size:12px; line-height:1.5; }
  .hint { color:#8fa4c0; margin-bottom:12px; }
</style>
</head>
<body>
<header>
  <h1>StayIn Local Side — DLU X ALFATEVE</h1>
  <p>Kontrak OpenAPI 3.0.3. Sumber tunggal untuk codegen tipe TypeScript di CMS dan aplikasi TV.
     Endpoint <code>/api/v1/stream/{movieID}</code> mengembalikan berkas mentah dengan dukungan byte-range.</p>
</header>
<main>
  <nav id="toc"></nav>
  <section>
    <p class="hint">Spesifikasi mentah tersedia di <a href="/swagger/openapi.yaml" style="color:#9ec1e8">/swagger/openapi.yaml</a></p>
    <pre id="spec">memuat…</pre>
  </section>
</main>
<script>
fetch('/swagger/openapi.yaml').then(r => r.text()).then(text => {
  document.getElementById('spec').textContent = text;

  const toc = document.getElementById('toc');
  const lines = text.split('\n');
  let inPaths = false;
  for (const line of lines) {
    if (line === 'paths:') { inPaths = true; continue; }
    if (inPaths && /^[a-z]/.test(line)) { inPaths = false; }
    if (!inPaths) continue;

    const pathMatch = line.match(/^  (\/[^:]+):\s*$/);
    if (pathMatch) {
      const path = pathMatch[1];
      const link = document.createElement('a');
      link.href = '#' + path;
      link.textContent = path;
      link.title = path;
      toc.appendChild(link);
      continue;
    }
    const methodMatch = line.match(/^    (get|post|put|patch|delete|head):/);
    if (methodMatch) {
      const link = document.createElement('a');
      link.href = '#' + methodMatch[1];
      link.innerHTML = '<span class="m">' + methodMatch[1].toUpperCase() + '</span>' + '';
      toc.appendChild(link);
    }
  }
}).catch(err => {
  document.getElementById('spec').textContent = 'Gagal memuat spesifikasi: ' + err;
});
</script>
</body>
</html>`

// SwaggerIndex serves the offline API reference.
func SwaggerIndex(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerIndex))
}

// SwaggerSpec serves the raw OpenAPI document used for code generation.
func SwaggerSpec(c *gin.Context) {
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", docs.OpenAPI)
}
