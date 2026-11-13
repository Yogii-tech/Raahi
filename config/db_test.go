package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func TestIsProductionEnv(t *testing.T) {
	tests := []struct {
		appEnv string
		want   bool
	}{
		{"production", true},
		{"PRODUCTION", true},
		{"  production  ", true},
		{"prod", true},
		{"PROD", true},
		{"staging", true},
		{" Staging ", true},
		{"", false},
		{"development", false},
		{"dev", false},
		{"local", false},
		{"test", false},
	}

	for _, tc := range tests {
		if got := IsProductionEnv(tc.appEnv); got != tc.want {
			t.Errorf("IsProductionEnv(%q) = %v, want %v", tc.appEnv, got, tc.want)
		}
	}
}

func TestBuildMongoClientOptions_ProductionGuards(t *testing.T) {
	invalidProdCases := []struct {
		name string
		uri  string
	}{
		{"empty MONGO_URI", ""},
		{"whitespace MONGO_URI", "   "},
		{"localhost default", "mongodb://localhost:27017/"},
		{"localhost uppercase with creds", "mongodb://admin:secret@LOCALHOST:27017/Raahi?authSource=admin"},
		{"127.0.0.1 loopback", "mongodb://127.0.0.1:27017/Raahi"},
		{"127.0.0.2 loopback range", "mongodb://127.0.0.2:27017/Raahi"},
		{"IPv6 loopback", "mongodb://[::1]:27017/Raahi"},
		{"IPv4-mapped IPv6 loopback", "mongodb://[::ffff:127.0.0.1]:27017/Raahi"},
		{"0.0.0.0 unspecified", "mongodb://0.0.0.0:27017/Raahi"},
		{"multi-host containing localhost", "mongodb://db1.example.com:27017,localhost:27017/Raahi?replicaSet=rs0"},
		{"localhost with trailing dot", "mongodb://localhost.:27017/Raahi"},
		{"subdomain of localhost", "mongodb://mongo.localhost:27017/Raahi"},
		{"srv localhost", "mongodb+srv://user:pass@localhost/Raahi"},
		{"unix socket path", "mongodb://%2Ftmp%2Fmongodb-27017.sock/Raahi"},
		{"malformed URI", "not-a-valid-mongo-uri"},
	}

	for _, tc := range invalidProdCases {
		t.Run("rejects_"+tc.name, func(t *testing.T) {
			if _, err := BuildMongoClientOptions(tc.uri, "production"); err == nil {
				t.Fatalf("expected error in production for %s (%q), got nil", tc.name, tc.uri)
			}
		})
	}

	// Production aliases ("PRODUCTION", "prod", "staging") must also enforce URI guards.
	for _, env := range []string{"PRODUCTION", "prod", "staging"} {
		t.Run("alias_"+env, func(t *testing.T) {
			if _, err := BuildMongoClientOptions("", env); err == nil {
				t.Fatalf("expected empty MONGO_URI to fail for APP_ENV=%q", env)
			}
			if _, err := BuildMongoClientOptions("mongodb://localhost:27017/", env); err == nil {
				t.Fatalf("expected localhost MONGO_URI to fail for APP_ENV=%q", env)
			}
		})
	}
}

func TestBuildMongoClientOptions_EnforcesReplicaDurability(t *testing.T) {
	// Even if the URI sets weaker query params (w=1, journal=false, retryWrites=false),
	// BuildMongoClientOptions must override them with durable majority+journal settings.
	prodURI := "mongodb://user:pass@node1.db.internal:27017,node2.db.internal:27017,node3.db.internal:27017/Raahi?replicaSet=rs0&w=1&journal=false&retryWrites=false&retryReads=false"
	for _, env := range []string{"production", "prod", "staging"} {
		opts, err := BuildMongoClientOptions(prodURI, env)
		if err != nil {
			t.Fatalf("expected valid multi-node URI in env=%q to succeed, got: %v", env, err)
		}

		if opts.WriteConcern == nil {
			t.Fatalf("expected WriteConcern to be non-nil (env=%q)", env)
		}
		if gotW := opts.WriteConcern.GetW(); gotW != "majority" {
			t.Errorf("env=%q WriteConcern.GetW() = %v, want \"majority\"", env, gotW)
		}
		if !opts.WriteConcern.GetJ() {
			t.Errorf("env=%q WriteConcern.GetJ() = false, want true (journaling required)", env)
		}
		if opts.RetryWrites == nil || !*opts.RetryWrites {
			t.Errorf("env=%q RetryWrites = %v, want true", env, opts.RetryWrites)
		}
		if opts.RetryReads == nil || !*opts.RetryReads {
			t.Errorf("env=%q RetryReads = %v, want true", env, opts.RetryReads)
		}
		if opts.ReadPreference == nil || opts.ReadPreference.Mode() != readpref.PrimaryMode {
			t.Errorf("env=%q ReadPreference = %v, want PrimaryMode", env, opts.ReadPreference)
		}
		if opts.ReadConcern == nil || opts.ReadConcern.Level != readconcern.Majority().Level {
			t.Errorf("env=%q ReadConcern = %+v, want majority", env, opts.ReadConcern)
		}
		if opts.MaxPoolSize == nil || *opts.MaxPoolSize != 10 {
			t.Errorf("env=%q MaxPoolSize = %v, want 10", env, opts.MaxPoolSize)
		}
		if opts.MinPoolSize == nil || *opts.MinPoolSize != 2 {
			t.Errorf("env=%q MinPoolSize = %v, want 2", env, opts.MinPoolSize)
		}
	}
}

func TestBuildMongoClientOptions_DevelopmentFallback(t *testing.T) {
	for _, env := range []string{"", "development"} {
		opts, err := BuildMongoClientOptions("", env)
		if err != nil {
			t.Fatalf("expected empty URI in env=%q to fall back cleanly, got error: %v", env, err)
		}
		if len(opts.Hosts) != 1 || opts.Hosts[0] != "localhost:27017" {
			t.Errorf("env=%q fallback hosts = %v, want [localhost:27017]", env, opts.Hosts)
		}
		if opts.WriteConcern == nil || opts.WriteConcern.GetW() != "majority" || !opts.WriteConcern.GetJ() {
			t.Errorf("expected durable majority+journal WriteConcern in dev fallback too, got %+v", opts.WriteConcern)
		}
		if opts.RetryWrites == nil || !*opts.RetryWrites {
			t.Errorf("expected RetryWrites=true in dev fallback")
		}
	}
}

func TestIsLocalhostMongoURI_RemoteAndSRVHosts(t *testing.T) {
	remoteURIs := []string{
		"mongodb+srv://user:pass@cluster0.abcde.mongodb.net/Raahi?retryWrites=true&w=majority",
		"mongodb://user:pass@node1.db.internal:27017,node2.db.internal:27017/Raahi?replicaSet=rs0",
	}
	for _, uri := range remoteURIs {
		if IsLocalhostMongoURI(uri) {
			t.Errorf("IsLocalhostMongoURI(%q) = true, want false", uri)
		}
	}

	// Verify remote SRV host does not trip the pre-parse localhost guard.
	if _, err := BuildMongoClientOptions("mongodb+srv://user:pass@cluster0.invalid.example/Raahi", "production"); err != nil {
		if strings.Contains(err.Error(), "localhost/loopback") {
			t.Errorf("remote SRV host triggered localhost error unexpectedly: %v", err)
		}
	}
}

func TestClassifyTopology(t *testing.T) {
	tests := []struct {
		name        string
		info        TopologyInfo
		isProd      bool
		wantMode    string
		wantWarning bool
		wantSubstr  string
	}{
		{
			name: "3-node replica set in production",
			info: TopologyInfo{
				SetName: "atlas-shard-0",
				Hosts:   []string{"n1:27017", "n2:27017", "n3:27017"},
				Primary: "n1:27017",
			},
			isProd:      true,
			wantMode:    "replica_set",
			wantWarning: false,
			wantSubstr:  "multi-node replica set",
		},
		{
			name: "2-node replica set warns about quorum",
			info: TopologyInfo{
				SetName: "rs0",
				Hosts:   []string{"n1:27017", "n2:27017"},
				Primary: "n1:27017",
			},
			isProd:      true,
			wantMode:    "replica_set_degraded",
			wantWarning: true,
			wantSubstr:  "2-node replica set",
		},
		{
			name: "single-node replica set warns loudly in production",
			info: TopologyInfo{
				SetName: "rs0",
				Hosts:   []string{"n1:27017"},
				Primary: "n1:27017",
			},
			isProd:      true,
			wantMode:    "single_node_replica_set",
			wantWarning: true,
			wantSubstr:  "PRODUCTION WARNING",
		},
		{
			name: "standalone node in production emits critical warning",
			info: TopologyInfo{
				SetName:  "",
				IsMaster: true,
			},
			isProd:      true,
			wantMode:    "standalone",
			wantWarning: true,
			wantSubstr:  "STANDALONE node",
		},
		{
			name: "standalone node in development is informational",
			info: TopologyInfo{
				SetName:  "",
				IsMaster: true,
			},
			isProd:      false,
			wantMode:    "standalone",
			wantWarning: false,
			wantSubstr:  "standalone node",
		},
		{
			name: "sharded cluster via mongos",
			info: TopologyInfo{
				Msg: "isdbgrid",
			},
			isProd:      true,
			wantMode:    "sharded",
			wantWarning: false,
			wantSubstr:  "sharded cluster",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := ClassifyTopology(tc.info, tc.isProd)
			if rep.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", rep.Mode, tc.wantMode)
			}
			if rep.IsWarning != tc.wantWarning {
				t.Errorf("IsWarning = %v, want %v (msg=%q)", rep.IsWarning, tc.wantWarning, rep.LogMessage)
			}
			if !strings.Contains(rep.LogMessage, tc.wantSubstr) {
				t.Errorf("LogMessage %q does not contain %q", rep.LogMessage, tc.wantSubstr)
			}
		})
	}
}

func TestInspectTopology_HelloAndIsMasterFallback(t *testing.T) {
	t.Run("uses hello when supported", func(t *testing.T) {
		var cmds []string
		rep := inspectTopology(context.Background(), func(_ context.Context, cmd bson.D, out interface{}) error {
			cmds = append(cmds, cmd[0].Key)
			info := out.(*TopologyInfo)
			info.SetName = "atlas-rs"
			info.Hosts = []string{"a:27017", "b:27017", "c:27017"}
			info.Primary = "a:27017"
			return nil
		}, true)
		if len(cmds) != 1 || cmds[0] != "hello" {
			t.Errorf("expected only [hello], got %v", cmds)
		}
		if rep.Mode != "replica_set" || rep.IsWarning {
			t.Errorf("unexpected report: %+v", rep)
		}
	})

	t.Run("falls back to isMaster when hello fails", func(t *testing.T) {
		var cmds []string
		rep := inspectTopology(context.Background(), func(_ context.Context, cmd bson.D, out interface{}) error {
			cmds = append(cmds, cmd[0].Key)
			if cmd[0].Key == "hello" {
				return errors.New("no such command: 'hello'")
			}
			info := out.(*TopologyInfo)
			info.SetName = ""
			info.IsMaster = true
			return nil
		}, true)
		if len(cmds) != 2 || cmds[0] != "hello" || cmds[1] != "isMaster" {
			t.Errorf("expected [hello isMaster], got %v", cmds)
		}
		if rep.Mode != "standalone" || !rep.IsWarning {
			t.Errorf("expected standalone warning in prod, got %+v", rep)
		}
	})

	t.Run("handles both commands failing cleanly", func(t *testing.T) {
		rep := inspectTopology(context.Background(), func(_ context.Context, _ bson.D, _ interface{}) error {
			return errors.New("connection refused")
		}, true)
		if rep.Mode != "unknown" || !rep.IsWarning {
			t.Errorf("expected unknown warning report, got %+v", rep)
		}
	})
}

func TestCollectionIndexSpecs_NoAccidentalTTLOnPersistentCollections(t *testing.T) {
	specs := CollectionIndexSpecs()
	if len(specs) == 0 {
		t.Fatal("expected non-empty CollectionIndexSpecs")
	}

	seenCollections := make(map[string]bool)
	notifHas30DayTTL := false
	usersHasUniquePhone := false

	for _, spec := range specs {
		seenCollections[spec.Collection] = true
		for _, idx := range spec.Models {
			hasTTL := idx.Options != nil && idx.Options.ExpireAfterSeconds != nil
			if spec.Collection != "notifications" && hasTTL {
				t.Errorf("collection %q must NOT have a TTL index (ExpireAfterSeconds=%d)",
					spec.Collection, *idx.Options.ExpireAfterSeconds)
			}
			if spec.Collection == "notifications" && hasTTL {
				if *idx.Options.ExpireAfterSeconds == 30*24*3600 {
					notifHas30DayTTL = true
				} else {
					t.Errorf("notifications TTL = %d, want 30 days (%d)", *idx.Options.ExpireAfterSeconds, 30*24*3600)
				}
			}
			if spec.Collection == "users" && idx.Options != nil && idx.Options.Unique != nil && *idx.Options.Unique {
				if keys, ok := idx.Keys.(bson.D); ok && len(keys) == 1 && keys[0].Key == "phone_number" {
					usersHasUniquePhone = true
				}
			}
		}
	}

	for _, required := range []string{"users", "rides", "bookings", "chats", "notifications", "reviews", "recent_routes"} {
		if !seenCollections[required] {
			t.Errorf("missing index specs for collection %q", required)
		}
	}
	if !notifHas30DayTTL {
		t.Errorf("expected notifications collection to retain its 30-day TTL index")
	}
	if !usersHasUniquePhone {
		t.Errorf("expected users collection to retain unique phone_number index")
	}
}
