package config

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

const (
	defaultDevMongoURI = "mongodb://localhost:27017/"
	defaultDBName      = "Raahi"
)

var DB *mongo.Client
var Database *mongo.Database

// TopologyInfo holds the subset of MongoDB `hello` / `isMaster` fields used to
// classify whether the connected server is a replica set, sharded router, or standalone node.
type TopologyInfo struct {
	SetName           string   `bson:"setName"`
	Hosts             []string `bson:"hosts"`
	Passives          []string `bson:"passives"`
	Arbiters          []string `bson:"arbiters"`
	Primary           string   `bson:"primary"`
	Me                string   `bson:"me"`
	Msg               string   `bson:"msg"` // "isdbgrid" when connected via mongos
	IsWritablePrimary bool     `bson:"isWritablePrimary"`
	IsMaster          bool     `bson:"ismaster"`
}

// TopologyReport summarizes the detected MongoDB cluster topology for startup logging and checks.
type TopologyReport struct {
	Mode       string
	SetName    string
	DataNodes  int
	Arbiters   int
	Primary    string
	IsWarning  bool
	LogMessage string
}

// CollectionIndexSpec pairs a MongoDB collection name with its required index models.
type CollectionIndexSpec struct {
	Collection string
	Models     []mongo.IndexModel
}

// IsProductionEnv reports whether the provided APP_ENV string represents a
// deployed non-development environment ("production", "prod", or "staging").
func IsProductionEnv(appEnv string) bool {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "production", "prod", "staging":
		return true
	default:
		return false
	}
}

func uriAuthorityHosts(uri string) []string {
	rest := strings.TrimSpace(uri)
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
	}
	if idx := strings.IndexAny(rest, "/?"); idx >= 0 {
		rest = rest[:idx]
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if rest == "" {
		return nil
	}
	parts := strings.Split(rest, ",")
	hosts := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	return hosts
}

func extractHostOnly(hostPort string) string {
	h := strings.TrimSpace(hostPort)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func isLocalMongoHost(hostPort string) bool {
	raw := strings.TrimSpace(hostPort)
	if raw == "" {
		return false
	}
	if unescaped, err := url.PathUnescape(raw); err == nil {
		if strings.Contains(unescaped, "/") || strings.HasSuffix(strings.ToLower(unescaped), ".sock") {
			return true
		}
	}
	host := strings.TrimSuffix(strings.ToLower(extractHostOnly(raw)), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// IsLocalhostMongoURI reports whether a MongoDB URI targets localhost, loopback,
// unspecified address (0.0.0.0 / ::), or a local Unix socket.
func IsLocalhostMongoURI(uri string) bool {
	for _, h := range uriAuthorityHosts(uri) {
		if isLocalMongoHost(h) {
			return true
		}
	}
	return false
}

// BuildMongoClientOptions validates the MongoDB connection URI for the current
// environment and builds durable, replica-set-safe ClientOptions.
//
// In deployed environments (APP_ENV=production|prod|staging), MONGO_URI must be
// non-empty and must not point to localhost/loopback. WriteConcern is enforced
// to majority + journaling (w: "majority", j: true) with retryable writes/reads
// and primary read preference.
func BuildMongoClientOptions(rawURI, appEnv string) (*options.ClientOptions, error) {
	isProd := IsProductionEnv(appEnv)
	trimmedEnv := strings.TrimSpace(appEnv)
	uri := strings.TrimSpace(rawURI)

	if uri == "" {
		if isProd {
			return nil, fmt.Errorf("MONGO_URI environment variable is required when APP_ENV=%q", trimmedEnv)
		}
		uri = defaultDevMongoURI
	}

	if isProd && IsLocalhostMongoURI(uri) {
		return nil, fmt.Errorf("MONGO_URI must not point to localhost/loopback when APP_ENV=%q", trimmedEnv)
	}

	clientOptions := options.Client().ApplyURI(uri)
	if err := clientOptions.Validate(); err != nil {
		return nil, fmt.Errorf("invalid MONGO_URI: %w", err)
	}

	if isProd {
		for _, h := range clientOptions.Hosts {
			if isLocalMongoHost(h) {
				return nil, fmt.Errorf("MONGO_URI resolves to localhost/loopback host %q when APP_ENV=%q", h, trimmedEnv)
			}
		}
	}

	// Enforce durable replica-set-safe options AFTER ApplyURI so weak query string
	// defaults cannot downgrade write durability or read consistency.
	clientOptions.
		SetAppName("raahi-backend").
		SetWriteConcern(writeconcern.Majority().WithOptions(writeconcern.J(true))).
		SetReadPreference(readpref.Primary()).
		SetRetryWrites(true).
		SetRetryReads(true).
		SetConnectTimeout(10 * time.Second).
		SetServerSelectionTimeout(10 * time.Second).
		SetMaxPoolSize(10).
		SetMinPoolSize(2)

	if isProd {
		clientOptions.SetReadConcern(readconcern.Majority())
	}

	return clientOptions, nil
}

// ClassifyTopology evaluates MongoDB `hello`/`isMaster` metadata and formats a
// clear startup diagnostic indicating whether replication redundancy is active.
func ClassifyTopology(info TopologyInfo, isProd bool) TopologyReport {
	if strings.EqualFold(strings.TrimSpace(info.Msg), "isdbgrid") {
		return TopologyReport{
			Mode:       "sharded",
			IsWarning:  false,
			LogMessage: "✅ MongoDB topology: sharded cluster (mongos)",
		}
	}

	setName := strings.TrimSpace(info.SetName)
	dataNodes := len(info.Hosts) + len(info.Passives)
	arbiters := len(info.Arbiters)

	if setName != "" {
		if dataNodes >= 3 {
			return TopologyReport{
				Mode:       "replica_set",
				SetName:    setName,
				DataNodes:  dataNodes,
				Arbiters:   arbiters,
				Primary:    info.Primary,
				IsWarning:  false,
				LogMessage: fmt.Sprintf("✅ MongoDB topology: multi-node replica set (setName=%q, dataNodes=%d, arbiters=%d, primary=%q)", setName, dataNodes, arbiters, info.Primary),
			}
		}
		if dataNodes == 2 {
			return TopologyReport{
				Mode:       "replica_set_degraded",
				SetName:    setName,
				DataNodes:  dataNodes,
				Arbiters:   arbiters,
				Primary:    info.Primary,
				IsWarning:  true,
				LogMessage: fmt.Sprintf("⚠️ MongoDB topology: 2-node replica set (setName=%q, dataNodes=2, arbiters=%d, primary=%q) — recommend >=3 data-bearing nodes for fault-tolerant majority failover", setName, arbiters, info.Primary),
			}
		}
		if isProd {
			return TopologyReport{
				Mode:       "single_node_replica_set",
				SetName:    setName,
				DataNodes:  dataNodes,
				Arbiters:   arbiters,
				Primary:    info.Primary,
				IsWarning:  true,
				LogMessage: fmt.Sprintf("⚠️ PRODUCTION WARNING: MongoDB is running as a single-node replica set (setName=%q, dataNodes=%d) — NO failover redundancy! Configure a >=3-node replica set in production.", setName, dataNodes),
			}
		}
		return TopologyReport{
			Mode:       "single_node_replica_set",
			SetName:    setName,
			DataNodes:  dataNodes,
			Arbiters:   arbiters,
			Primary:    info.Primary,
			IsWarning:  true,
			LogMessage: fmt.Sprintf("⚠️ MongoDB topology: single-node replica set (setName=%q, dataNodes=%d) — no failover redundancy (acceptable for local dev)", setName, dataNodes),
		}
	}

	if isProd {
		return TopologyReport{
			Mode:       "standalone",
			DataNodes:  1,
			IsWarning:  true,
			LogMessage: "⚠️ PRODUCTION WARNING: MongoDB is running as a STANDALONE node (no replica set / setName detected)! Data is NOT replicated across nodes, automatic failover is unavailable, and retryable writes are ignored. Migrate to a >=3-node replica set (e.g. MongoDB Atlas) immediately.",
		}
	}

	return TopologyReport{
		Mode:       "standalone",
		DataNodes:  1,
		IsWarning:  false,
		LogMessage: "ℹ️ MongoDB topology: standalone node (no replica set) — acceptable for local development",
	}
}

func inspectTopology(
	ctx context.Context,
	runAdminCmd func(context.Context, bson.D, interface{}) error,
	isProd bool,
) TopologyReport {
	var info TopologyInfo
	err := runAdminCmd(ctx, bson.D{{Key: "hello", Value: 1}}, &info)
	if err != nil {
		// Fall back to `isMaster` for MongoDB servers older than 4.4.2.
		fallbackErr := runAdminCmd(ctx, bson.D{{Key: "isMaster", Value: 1}}, &info)
		if fallbackErr != nil {
			return TopologyReport{
				Mode:       "unknown",
				IsWarning:  true,
				LogMessage: fmt.Sprintf("⚠️ Unable to inspect MongoDB replica-set topology (hello error: %v; isMaster error: %v)", err, fallbackErr),
			}
		}
	}
	return ClassifyTopology(info, isProd)
}

func checkClusterTopology(client *mongo.Client, isProd bool) TopologyReport {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	adminDB := client.Database("admin")
	report := inspectTopology(ctx, func(c context.Context, cmd bson.D, out interface{}) error {
		return adminDB.RunCommand(c, cmd).Decode(out)
	}, isProd)
	log.Println(report.LogMessage)
	return report
}

func ConnectDB() {
	appEnv := os.Getenv("APP_ENV")
	isProd := IsProductionEnv(appEnv)
	rawURI := os.Getenv("MONGO_URI")

	if strings.TrimSpace(rawURI) == "" && !isProd {
		log.Printf("ℹ️ MONGO_URI not set — using local development fallback %s", defaultDevMongoURI)
	}

	clientOptions, err := BuildMongoClientOptions(rawURI, appEnv)
	if err != nil {
		log.Fatalf("❌ FATAL MongoDB configuration error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatalf("❌ FATAL MongoDB connection error: %v", err)
	}

	// Ping primary to verify an active, writable MongoDB server is reachable.
	pingErr := client.Ping(ctx, readpref.Primary())
	if pingErr != nil {
		if isProd {
			log.Fatalf("❌ FATAL MongoDB ping failed in %q (refusing to start with broken DB): %v", strings.TrimSpace(appEnv), pingErr)
		}
		log.Printf("⚠️ MongoDB ping warning (dev mode — container will remain running): %v", pingErr)
	} else {
		log.Printf("✅ MongoDB connected successfully (hosts=%v, writeConcern=majority+journal, retryWrites=true)", clientOptions.Hosts)
	}

	DB = client
	Database = client.Database(defaultDBName)
	if Database == nil {
		log.Fatal("❌ MongoDB database handle is nil")
	}

	if pingErr == nil {
		checkClusterTopology(client, isProd)
		InitializeIndexes()
	}
}

func ensureCollectionIndexes(ctx context.Context, collectionName string, models []mongo.IndexModel) {
	if _, err := Database.Collection(collectionName).Indexes().CreateMany(ctx, models); err != nil {
		log.Printf("⚠️ Failed to create %s indexes: %v", collectionName, err)
	}
}

// CollectionIndexSpecs returns the declarative index specifications for all collections.
// IMPORTANT: Only ephemeral collections (like `notifications`) may configure TTL expiration
// (`ExpireAfterSeconds`). Persistent business data (`users`, `rides`, `bookings`, `chats`,
// `reviews`, `recent_routes`) must NEVER carry TTL indexes.
func CollectionIndexSpecs() []CollectionIndexSpec {
	return []CollectionIndexSpec{
		{
			// Users: Phone number unique, role search, and admin verification queue (NO TTL)
			Collection: "users",
			Models: []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "phone_number", Value: 1}},
					Options: options.Index().SetUnique(true),
				},
				{Keys: bson.D{{Key: "role", Value: 1}}},
				{Keys: bson.D{{Key: "verification_status", Value: 1}, {Key: "submitted_at", Value: -1}}},
			},
		},
		{
			// Rides: Searchable by route, status/date, driver, and time (NO TTL)
			Collection: "rides",
			Models: []mongo.IndexModel{
				{Keys: bson.D{{Key: "driverId", Value: 1}}},
				{Keys: bson.D{{Key: "status", Value: 1}}},
				{Keys: bson.D{{Key: "pickup", Value: 1}, {Key: "dropoff", Value: 1}}},
				{Keys: bson.D{{Key: "createdAt", Value: -1}}},
				{Keys: bson.D{{Key: "status", Value: 1}, {Key: "date", Value: 1}}},
				{Keys: bson.D{{Key: "driverId", Value: 1}, {Key: "createdAt", Value: -1}}},
				{Keys: bson.D{{Key: "driverId", Value: 1}, {Key: "status", Value: 1}}},
			},
		},
		{
			// Bookings: Relational links, status filters, and passenger history (NO TTL)
			Collection: "bookings",
			Models: []mongo.IndexModel{
				{Keys: bson.D{{Key: "rideId", Value: 1}}},
				{Keys: bson.D{{Key: "passengerId", Value: 1}}},
				{Keys: bson.D{{Key: "type", Value: 1}, {Key: "status", Value: 1}}},
				{Keys: bson.D{{Key: "createdAt", Value: -1}}},
				{Keys: bson.D{{Key: "rideId", Value: 1}, {Key: "status", Value: 1}}},
				{Keys: bson.D{{Key: "passengerId", Value: 1}, {Key: "createdAt", Value: -1}}},
			},
		},
		{
			// Chats: Chronological lookup per booking (NO TTL)
			Collection: "chats",
			Models: []mongo.IndexModel{
				{Keys: bson.D{{Key: "bookingId", Value: 1}, {Key: "createdAt", Value: 1}}},
			},
		},
		{
			// Notifications: Per-user feed + 30-day TTL auto-expiration
			Collection: "notifications",
			Models: []mongo.IndexModel{
				{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
				{
					Keys:    bson.D{{Key: "createdAt", Value: 1}},
					Options: options.Index().SetExpireAfterSeconds(30 * 24 * 3600),
				},
			},
		},
		{
			// Reviews: One review per passenger per ride + driver review feed (NO TTL)
			Collection: "reviews",
			Models: []mongo.IndexModel{
				{
					Keys:    bson.D{{Key: "rideId", Value: 1}, {Key: "passengerId", Value: 1}},
					Options: options.Index().SetUnique(true),
				},
				{Keys: bson.D{{Key: "driverId", Value: 1}, {Key: "createdAt", Value: -1}}},
			},
		},
		{
			// Recent routes: Per-user search history (NO TTL)
			Collection: "recent_routes",
			Models: []mongo.IndexModel{
				{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
			},
		},
	}
}

func InitializeIndexes() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, spec := range CollectionIndexSpecs() {
		ensureCollectionIndexes(ctx, spec.Collection, spec.Models)
	}

	log.Println("⚡ Database indexes initialized")
}
