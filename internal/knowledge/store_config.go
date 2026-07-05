package knowledge

// StoreConfig describes how to connect to a vector store (local or remote).
// It mirrors the JSON structure in the user's config.json under
// knowledge.vector_stores[]. Go-side the config is immutable after creation;
// changes require closing the store and opening a new one.
type StoreConfig struct {
	// Name is the user-facing identifier (e.g. "local", "milvus-team").
	// Must be unique across all stores.
	Name string `json:"name"`

	// Type is "local" or "remote".
	Type string `json:"type"`

	// Driver selects the concrete implementation. For remote stores this
	// is the vendor name: "milvus", "qdrant", "chroma", "weaviate", "pinecone".
	// For local stores this is "local".
	Driver string `json:"driver,omitempty"`

	// ---- local store fields ----

	// Path is the directory where local vector files are stored.
	// Only used when Type == "local".
	Path string `json:"path,omitempty"`

	// ---- remote store fields ----

	// Host is the hostname or IP of the remote service.
	Host string `json:"host,omitempty"`

	// Port is the TCP port of the remote service.
	Port int `json:"port,omitempty"`

	// URL is the full HTTP endpoint (alternative to Host+Port for REST-based
	// stores like Qdrant, Chroma, Pinecone).
	URL string `json:"url,omitempty"`

	// Collection is the collection/namespace/class name within the remote
	// vector database.
	Collection string `json:"collection,omitempty"`

	// Database is an optional database/tenant name (used by Milvus).
	Database string `json:"database,omitempty"`

	// APIKey is an optional authentication key.
	APIKey string `json:"api_key,omitempty"`

	// Auth holds optional username/password (used by Milvus).
	Auth *StoreAuth `json:"auth,omitempty"`
}

// StoreAuth holds credentials for stores that require username/password.
type StoreAuth struct {
	User     string `json:"user"`
	Password string `json:"password"`
}
