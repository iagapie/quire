// Package certs embeds the Amazon RDS / DocumentDB global CA bundle.
package certs

import _ "embed"

// AWSGlobalBundle is https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem.
//
//go:embed aws-global-bundle.pem
var AWSGlobalBundle []byte
