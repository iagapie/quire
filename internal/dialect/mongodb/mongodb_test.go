package mongodb

import (
	"slices"
	"testing"

	"github.com/iagapie/quire/internal/dialect"
)

func TestRegistered(t *testing.T) {
	for _, id := range []string{MongoDB, DocumentDB} {
		d, ok := dialect.Get(id)
		if !ok {
			t.Fatalf("%s is not registered", id)
		}
		info := d.Info()
		if !info.Shell || !info.Databases || info.Schemas || info.DefaultPort != 27017 || !info.SSH || !IsShell(id) {
			t.Errorf("%s: info = %+v", id, info)
		}
		if _, sql := d.(dialect.SQLDriver); sql {
			t.Errorf("%s must not be a SQL driver", id)
		}
		keys := make([]string, 0, len(info.Fields))
		for _, f := range info.Fields {
			keys = append(keys, f.Key)
		}
		for _, k := range []string{OptReadPreference, OptDirect, OptReplicaSet, OptAuthSource, OptAuthMechanism, OptExtraDatabases} {
			if !slices.Contains(keys, k) {
				t.Errorf("%s: no %s setting", id, k)
			}
		}
	}
	doc, _ := dialect.Get(DocumentDB)
	if i := doc.Info(); i.TLSDefault != "verify-full" || !i.AWSBundle {
		t.Errorf("DocumentDB TLS defaults = %q, %v", i.TLSDefault, i.AWSBundle)
	}
	if IsShell("postgres") {
		t.Error("postgres is not a shell database")
	}
}
