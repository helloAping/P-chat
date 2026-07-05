package knowledge

// IndexableExtensions is the set of file extensions that the
// indexer and scanner will process. Binaries, images, and
// archives are excluded 鈥?only plain-text source/doc/config
// files are indexed.
var IndexableExtensions = map[string]bool{
		// Documents
		".md": true, ".txt": true, ".markdown": true, ".rst": true, ".org": true,
		// Code
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
		".py": true, ".java": true, ".rs": true, ".cpp": true, ".c": true,
		".h": true, ".hpp": true, ".vue": true, ".svelte": true, ".astro": true,
		".cs": true, ".rb": true, ".php": true, ".swift": true, ".kt": true,
		".scala": true, ".sh": true, ".bash": true, ".ps1": true, ".bat": true,
		".sql": true, ".r": true, ".dart": true, ".lua": true, ".zig": true,
		".nim": true, ".ex": true, ".exs": true, ".elm": true, ".clj": true,
		".groovy": true, ".fs": true, ".fsx": true, ".erl": true, ".hrl": true,
		// Config / Data
		".json": true, ".yaml": true, ".yml": true, ".toml": true,
		".xml": true, ".ini": true, ".cfg": true, ".conf": true,
		".env": true, ".properties": true, ".editorconfig": true,
		// Web
		".html": true, ".htm": true, ".css": true, ".scss": true, ".less": true,
		// Other text
		".csv": true, ".tsv": true, ".log": true, ".diff": true, ".patch": true,
		".proto": true, ".graphql": true, ".gql": true, ".tf": true,
	}

type SearchResult struct {
	ChunkID    int64
	Source     string
	Content    string
	Similarity float32
	Rank       int
}
