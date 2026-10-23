// This go.mod keeps web/ out of the kubexray Go module: npm packages in
// node_modules can contain Go files, which Go tools would otherwise build,
// lint and scan as part of kubexray.
module github.com/kubexray/kubexray/web

go 1.27.3
