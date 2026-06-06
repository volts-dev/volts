package openapi

import "strings"

const scalarTemplate = `<!doctype html>
<html>
  <head>
    <title>API Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="__SPEC_URL__"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

// ScalarHTML 返回内嵌指定 spec URL 的 Scalar UI 页面。
func ScalarHTML(specURL string) string {
	return strings.ReplaceAll(scalarTemplate, "__SPEC_URL__", specURL)
}
