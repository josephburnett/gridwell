package plugin

// NodeExportHeader is the gRPC metadata key (== HTTP/2 header) of the node
// export contract: a request carrying it addresses ONE plugin on the serving
// node (by uuid or config name) and is answered by that plugin's own service
// verbatim — local ids, its own Info/streams. Defined once here because both
// sides of the wire share it: internal/server routes on it (nodeexport.go)
// and internal/plugin/sshdial stamps it on every tunneled call.
const NodeExportHeader = "gridwell-plugin"
