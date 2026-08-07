# Test layout

This directory is reserved for black-box and end-to-end tests that exercise the
application through public interfaces (HTTP, generated Wails bindings, or a
running frontend).

The Go unit tests remain beside the Go package at the repository root. They use
`package main` and intentionally cover unexported implementation details. Moving
those files into this directory would create a different Go package and break
the application’s build contract. This split keeps paths stable while giving
integration tests a dedicated home.

## Verification

```powershell
go test ./...
cd frontend
npm.cmd run build
```
