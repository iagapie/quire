// Package mongodb registers MongoDB and Amazon DocumentDB. They are not SQL databases:
// their connections are opened by internal/docsession with the official MongoDB
// driver, and their editor tabs run mongo shell JavaScript (internal/shell). This
// package only describes them to the UI and names their connection settings.
package mongodb

import (
	"github.com/iagapie/quire/internal/dialect"
)

func init() {
	dialect.Register(Driver{info: mongoInfo()})
	dialect.Register(Driver{info: documentDBInfo()})
}

// Driver IDs.
const (
	MongoDB    = "mongodb"
	DocumentDB = "documentdb"
)

// Option keys of the connection settings (profile.Options).
const (
	OptReadPreference = "readPreference"
	OptDirect         = "directConnection"
	OptReplicaSet     = "replicaSet"
	OptAuthSource     = "authSource"
	OptAuthMechanism  = "authMechanism"
	// OptExtraDatabases lists databases shown even when they cannot be listed (comma separated).
	OptExtraDatabases = "extraDatabases"
)

// Driver describes a MongoDB-compatible database.
type Driver struct {
	info dialect.Info
}

// Info describes the database to the UI.
func (d Driver) Info() dialect.Info { return d.info }

// IsShell reports whether a driver id is one of the mongo shell databases.
func IsShell(id string) bool { return id == MongoDB || id == DocumentDB }

var readPreferences = []dialect.Choice{
	{Value: "", Label: "Default (primary)"},
	{Value: "primaryPreferred", Label: "Primary preferred"},
	{Value: "secondary", Label: "Secondary"},
	{Value: "secondaryPreferred", Label: "Secondary preferred"},
	{Value: "nearest", Label: "Nearest"},
}

func base() dialect.Info {
	return dialect.Info{
		Editor:          "JavaScript",
		Shell:           true,
		Document:        true,
		Databases:       true,
		DatabaseLabel:   "Database",
		Folders:         []string{"collection", "view"},
		DefaultPort:     27017,
		HostLabel:       "Hosts",
		HostPlaceholder: "host1:27017,host2:27017",
		SSH:             true,
		Upsert:          true,
		Index:           dialect.IndexFeatures{Editable: true},
	}
}

func fields(direct bool, mechanisms []dialect.Choice) []dialect.Field {
	def := "false"
	if direct {
		def = "true"
	}
	return []dialect.Field{
		{Key: OptReadPreference, Label: "Read preference", Section: "server", Type: "select", Options: readPreferences},
		{Key: OptDirect, Label: "Direct connection", Section: "server", Type: "bool", Default: def,
			Hint: "Talk only to the given host instead of discovering the replica set. Recommended for DocumentDB cluster endpoints."},
		{Key: OptAuthSource, Label: "Authentication database", Section: "auth", Type: "text", Placeholder: "admin",
			Hint: "Where the user is defined. Does not need to be readable."},
		{Key: OptAuthMechanism, Label: "Mechanism", Section: "auth", Type: "select", Options: mechanisms},
		{Key: OptReplicaSet, Label: "Replica set name", Section: "advanced", Type: "text", Placeholder: "rs0",
			Hint: "Only when Direct connection is off and the connection string does not name it (DocumentDB: rs0)."},
		{Key: OptExtraDatabases, Label: "Additional databases", Section: "advanced", Type: "text",
			Hint: "Comma separated. Shown in the tree when the user is not allowed to list databases."},
	}
}

func mongoInfo() dialect.Info {
	i := base()
	i.ID = MongoDB
	i.Label = "MongoDB"
	i.Description = "MongoDB 4.2+ and MongoDB Atlas: mongo shell JavaScript"
	i.URIPlaceholder = "mongodb://user:password@host:27017/?replicaSet=rs0 or mongodb+srv://cluster.example.net/"
	i.TLS = []string{"disable", "require", "verify-ca", "verify-full"}
	i.Fields = fields(false, []dialect.Choice{
		{Value: "", Label: "Negotiate"},
		{Value: "SCRAM-SHA-256", Label: "SCRAM-SHA-256"},
		{Value: "SCRAM-SHA-1", Label: "SCRAM-SHA-1"},
		{Value: "MONGODB-X509", Label: "X.509 (TLS client certificate)"},
	})
	i.Order = 70
	return i
}

func documentDBInfo() dialect.Info {
	i := base()
	i.ID = DocumentDB
	i.Label = "DocumentDB"
	i.Description = "Amazon DocumentDB 5.0+ (with MongoDB compatibility): mongo shell JavaScript"
	i.HostPlaceholder = "my-cluster.cluster-xxxx.eu-central-1.docdb.amazonaws.com"
	i.URIPlaceholder = "mongodb://user:password@my-cluster.cluster-xxxx.eu-central-1.docdb.amazonaws.com:27017/?tls=true"
	i.TLS = []string{"disable", "require", "verify-ca", "verify-full"}
	i.TLSDefault = "verify-full"
	i.AWSBundle = true
	i.Fields = fields(true, []dialect.Choice{
		{Value: "", Label: "Negotiate"},
		{Value: "SCRAM-SHA-1", Label: "SCRAM-SHA-1"},
		{Value: "SCRAM-SHA-256", Label: "SCRAM-SHA-256"},
	})
	i.Note = "DocumentDB clusters live in a VPC: connect from inside it, or through an SSH tunnel to a bastion. " +
		"Retryable writes are turned off, as DocumentDB does not support them."
	i.Order = 71
	return i
}
