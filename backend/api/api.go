// Package api содержит спецификацию OpenAPI, встроенную в бинарник и
// отдаваемую сервером на /api/openapi.yaml и /api/docs (Swagger UI).
package api

import _ "embed"

// Spec — OpenAPI 3.0 спецификация API кредитного конвейера.
//
//go:embed openapi.yaml
var Spec []byte
